// Package web exposes the authenticated local maintainer HTTP surface.
//
// The package deliberately depends on the small Backend interface only.  Git,
// GitHub, and maintenance implementation details stay in the store package.
package web

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxBodyBytes     = 256 * 1024
	maxHookBodyBytes = 16 * 1024

	// Inbound hook headers. The signature covers the timestamp and the exact
	// raw body, so a captured request cannot be replayed after the skew window
	// and a modified body cannot be re-signed without the secret.
	hookTimestampHeader = "X-Folio-Timestamp"
	hookEventIDHeader   = "X-Folio-Event-Id"
	hookSignatureHeader = "X-Folio-Signature"
	maxResponseBytes    = 512 * 1024
	maxPathBytes        = 2048
	maxStringBytes      = 256 * 1024

	sessionTTL       = 30 * time.Minute
	jobTTL           = 24 * time.Hour
	previewTTL       = 10 * time.Minute
	loginWindow      = time.Minute
	loginMaxAttempts = 5
	maxSessions      = 1024
	maxJobs          = 256
	maxPreviews      = 64

	pbkdf2Rounds = 240_000
	pbkdf2Bytes  = 32
	cookieName   = "pc_session"
)

var (
	safeRepositoryPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	safeModel          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,199}$`)
	safeJobID          = regexp.MustCompile(`^[0-9a-f-]{36}$`)
	safePreviewID      = regexp.MustCompile(`^[A-Za-z0-9_-]{20,100}$`)
	safeReportID       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	sensitiveKey       = regexp.MustCompile(`(?i)(pass(?:word)?|token|secret|credential|authorization|private[_ -]?key|device[_ -]?(?:code|key)|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|client[_ -]?secret)`)
	credentialURL      = regexp.MustCompile(`(?i)(https?://)([^/\s@]+)@`)
	bearerValue        = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	inboundHookRE      = regexp.MustCompile(`^/api/hooks/[a-z0-9][a-z0-9-]{0,47}$`)
	keyValueSecret     = regexp.MustCompile(`(?i)((?:access[_ -]?token|refresh[_ -]?token|device[_ -]?(?:code|key)|api[_ -]?key|client[_ -]?secret|token|secret|credential|private[_ -]?key|password|authorization)\s*[:=]\s*)[^\s,;]+`)
)

// Backend is the store contract consumed by the HTTP adapter.
type Backend interface {
	State() (map[string]any, error)
	GitHubStart() (map[string]any, error)
	Poll() (map[string]any, error)
	Logout() (map[string]any, error)
	GitHubRepositories() (map[string]any, error)
	ConnectRepository(fullName string) (map[string]any, error)
	SyncRepository() (map[string]any, error)
	RunMaintenance(mode, model, runner string) (map[string]any, error)
	RunAutomation(trigger, runner, model string) (map[string]any, error)
	ListAutomation() (map[string]any, error)
	AutomationRun(runID string) (map[string]any, error)
	ModelCatalog() map[string]any
	SchedulerTick() (map[string]any, error)
}

// ChannelBackend is the optional outbound-notification contract. It is
// separate from Backend so a backend that has no channels still satisfies the
// core adapter; the handlers answer 503 instead of failing to compile.
type ChannelBackend interface {
	ChannelState() (map[string]any, error)
	SaveBarkChannel(payload map[string]any) (map[string]any, error)
	TestBarkChannel(override map[string]any) (map[string]any, error)
}

// HookBackend is the optional inbound-trigger contract. Like ChannelBackend it
// is separate from Backend so the core adapter stays small.
type HookBackend interface {
	ListHooks() (map[string]any, error)
	CreateHook(name, action string) (map[string]any, error)
	UpdateHook(id string, enabled bool, name string) (map[string]any, error)
	RotateHookSecret(id string) (map[string]any, error)
	DeleteHook(id string) (map[string]any, error)
	AcceptHook(id string, timestamp int64, eventID, signature string, body []byte) (string, string)
}

// Options configures the local web boundary.
type Options struct {
	DataDir           string
	PasswordFile      string
	AssetsDir         string
	TrustedHosts      []string
	TrustedOrigins    []string
	CookieSecure      bool
	SchedulerEnabled  bool
	SchedulerInterval time.Duration

	// PasswordNotice, when set, receives the administrator password and its
	// file path exactly once, on the start that creates the password. Starts
	// that reuse an existing file never call it. The callback exists so the
	// process entrypoint can surface the bootstrap credential in its own log;
	// keep that log private because it now contains a credential.
	PasswordNotice func(password, path string)
}

type passwordAuth struct {
	hashPath string
	salt     []byte
	digest   []byte
}

type session struct {
	csrf     string
	expires  time.Time
	previews map[string]preview
}

type preview struct {
	target    string
	content   string
	digest    string
	reviewID  string
	createdAt time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	items    map[string]*session
	maxItems int
}

type loginAttempt struct {
	stamps []time.Time
}

type loginLimiter struct {
	mu       sync.Mutex
	items    map[string]loginAttempt
	maxItems int
}

type job struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	CreatedAt  string         `json:"created_at"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	Error      string         `json:"error,omitempty"`
}

type jobStore struct {
	mu       sync.Mutex
	items    map[string]*job
	active   map[string]string
	maxItems int
}

func (j job) public() map[string]any {
	result := map[string]any{
		"id": j.ID, "name": j.Name, "status": j.Status,
		"created_at": j.CreatedAt, "started_at": j.StartedAt,
	}
	if j.FinishedAt != "" {
		result["finished_at"] = j.FinishedAt
	}
	if j.Result != nil {
		result["result"] = j.Result
	}
	if j.Error != "" {
		result["error"] = j.Error
	}
	return result
}

// Server is an HTTP server adapter. Use Handler with net/http.Server so the
// caller can set read/write/idle timeouts at the process boundary.
type Server struct {
	store Backend
	opts  Options
	auth  passwordAuth

	sessions *sessionStore
	logins   *loginLimiter
	jobs     *jobStore

	schedulerMu   sync.Mutex
	scheduler     bool
	schedulerNext time.Time
}

// New initializes the password verifier and bounded in-memory service state.
func New(store Backend, opts Options) (*Server, error) {
	if store == nil {
		return nil, errors.New("web backend is required")
	}
	if opts.DataDir == "" {
		opts.DataDir = "."
	}
	if opts.PasswordFile == "" {
		opts.PasswordFile = filepath.Join(opts.DataDir, "secrets", "admin-password")
	}
	if opts.SchedulerInterval <= 0 {
		opts.SchedulerInterval = 24 * time.Hour
	}
	if opts.SchedulerInterval < time.Minute {
		opts.SchedulerInterval = time.Minute
	}
	if opts.AssetsDir == "" {
		opts.AssetsDir = "web"
	}
	hosts := normalizeHosts(opts.TrustedHosts)
	if len(hosts) == 0 {
		hosts = map[string]struct{}{"localhost": {}, "127.0.0.1": {}}
	}
	opts.TrustedHosts = sortedKeys(hosts)
	opts.TrustedOrigins = normalizeOrigins(opts.TrustedOrigins)
	auth, generatedPassword, err := loadPassword(opts.DataDir, opts.PasswordFile)
	if err != nil {
		return nil, err
	}
	if generatedPassword != "" && opts.PasswordNotice != nil {
		opts.PasswordNotice(generatedPassword, filepath.Clean(opts.PasswordFile))
	}
	return &Server{
		store:    store,
		opts:     opts,
		auth:     auth,
		sessions: &sessionStore{items: make(map[string]*session), maxItems: maxSessions},
		logins:   &loginLimiter{items: make(map[string]loginAttempt), maxItems: maxSessions},
		jobs:     &jobStore{items: make(map[string]*job), active: make(map[string]string), maxItems: maxJobs},
	}, nil
}

// Handler returns the complete standard-library HTTP handler.
func (s *Server) Handler() http.Handler { return s }

// StartScheduler starts automatic scheduled maintenance when enabled. Each
// tick pulls the checkout, organizes bounded Markdown edits, validates them,
// and pushes a commit. The next scheduled time is exposed for observability.
func (s *Server) StartScheduler(ctx context.Context) {
	if !s.opts.SchedulerEnabled {
		return
	}
	s.schedulerMu.Lock()
	if s.scheduler {
		s.schedulerMu.Unlock()
		return
	}
	s.scheduler = true
	interval := s.opts.SchedulerInterval
	s.schedulerNext = time.Now().Add(interval)
	s.schedulerMu.Unlock()
	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				s.schedulerMu.Lock()
				s.scheduler = false
				s.schedulerNext = time.Time{}
				s.schedulerMu.Unlock()
				return
			case <-timer.C:
				s.schedulerMu.Lock()
				s.schedulerNext = time.Now().Add(interval)
				s.schedulerMu.Unlock()
				s.startJob("scheduler:automation", func() (map[string]any, error) {
					return s.store.SchedulerTick()
				})
				timer.Reset(interval)
			}
		}
	}()
}

func (s *Server) schedulerNextAt() any {
	if !s.opts.SchedulerEnabled {
		return nil
	}
	s.schedulerMu.Lock()
	defer s.schedulerMu.Unlock()
	if s.schedulerNext.IsZero() {
		return nil
	}
	return s.schedulerNext.UTC().Format(time.RFC3339Nano)
}

// loadPassword returns the verifier plus the plaintext password only when this
// call created the password file. The second result is empty for every later
// start, so callers can announce the bootstrap credential exactly once.
func loadPassword(dataDir, passwordFile string) (passwordAuth, string, error) {
	passwordPath := filepath.Clean(passwordFile)
	if err := ensurePasswordParent(filepath.Dir(passwordPath)); err != nil {
		return passwordAuth{}, "", errors.New("admin password directory unavailable")
	}
	if info, statErr := os.Lstat(passwordPath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return passwordAuth{}, "", errors.New("admin password unavailable")
	}
	var generatedPassword string
	password, err := os.ReadFile(passwordPath)
	if errors.Is(err, os.ErrNotExist) {
		generated := make([]byte, 32)
		if _, err := rand.Read(generated); err != nil {
			return passwordAuth{}, "", errors.New("admin password unavailable")
		}
		password = []byte(encodePassword(generated))
		if err := writePrivateFile(passwordPath, append(password, '\n')); err != nil {
			return passwordAuth{}, "", errors.New("admin password unavailable")
		}
		generatedPassword = string(password)
	} else if err != nil {
		return passwordAuth{}, "", errors.New("admin password unavailable")
	}
	password = []byte(strings.TrimSpace(string(password)))
	if len(password) == 0 || len(password) > 4096 {
		return passwordAuth{}, "", errors.New("admin password unavailable")
	}
	_ = os.Chmod(passwordPath, 0o600)

	hashPath := filepath.Join(dataDir, "secrets", "admin-password.hash")
	if err := ensurePrivateDir(filepath.Dir(hashPath)); err != nil {
		return passwordAuth{}, "", errors.New("admin password hash unavailable")
	}
	var salt, digest []byte
	// A verifier cached for an earlier password must not shadow a password that
	// this start just generated; the plaintext file is authoritative.
	if generatedPassword == "" {
		if raw, readErr := os.ReadFile(hashPath); readErr == nil {
			var saved struct {
				Salt   string `json:"salt"`
				Digest string `json:"digest"`
			}
			if json.Unmarshal(raw, &saved) == nil {
				salt, _ = hex.DecodeString(saved.Salt)
				digest, _ = hex.DecodeString(saved.Digest)
			}
		}
	}
	if len(salt) < 16 || len(digest) != pbkdf2Bytes {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return passwordAuth{}, "", errors.New("admin password hash unavailable")
		}
		digest, err = derivePassword(string(password), salt)
		if err != nil {
			return passwordAuth{}, "", errors.New("admin password hash unavailable")
		}
		encoded, _ := json.Marshal(map[string]string{"salt": hex.EncodeToString(salt), "digest": hex.EncodeToString(digest)})
		if err := writePrivateFile(hashPath, append(encoded, '\n')); err != nil {
			return passwordAuth{}, "", errors.New("admin password hash unavailable")
		}
	}
	_ = os.Chmod(hashPath, 0o600)
	return passwordAuth{hashPath: hashPath, salt: salt, digest: digest}, generatedPassword, nil
}

func derivePassword(password string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, pbkdf2Rounds, pbkdf2Bytes)
}

func (p passwordAuth) verify(candidate string) bool {
	if len(candidate) > 4096 {
		return false
	}
	derived, err := derivePassword(candidate, p.salt)
	if err != nil || len(derived) != len(p.digest) {
		return false
	}
	return subtle.ConstantTimeCompare(derived, p.digest) == 1
}

func encodePassword(raw []byte) string { return strings.TrimRight(hex.EncodeToString(raw), "\n") }

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

// External secret mounts can be read-only and may reject chmod. Existing
// password files remain authoritative; generated passwords still require a
// writable parent and are checked by writePrivateFile.
func ensurePasswordParent(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("password directory is invalid")
	}
	_ = os.Chmod(path, 0o700)
	return nil
}

func writePrivateFile(path string, data []byte) error {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".maintainer-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func normalizeHosts(values []string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" || strings.ContainsAny(value, "\r\n\t/*") {
			continue
		}
		if host, _, err := net.SplitHostPort(value); err == nil {
			value = strings.Trim(host, "[]")
		} else if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			value = strings.Trim(value, "[]")
		}
		if parsed := net.ParseIP(value); parsed != nil {
			result[parsed.String()] = struct{}{}
			continue
		}
		if strings.Contains(value, ":") {
			continue
		}
		result[strings.TrimSuffix(value, ".")] = struct{}{}
	}
	return result
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func normalizeOrigins(values []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized, ok := normalizeOrigin(value)
		if !ok {
			continue
		}
		if _, exists := seen[normalized]; !exists {
			seen[normalized] = struct{}{}
			result = append(result, normalized)
		}
	}
	sort.Strings(result)
	return result
}

func normalizeOrigin(raw string) (string, bool) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	port, err := effectivePort(parsed.Scheme, parsed.Port())
	if err != nil {
		return "", false
	}
	return parsed.Scheme + "://" + formatHost(host) + ":" + strconv.Itoa(port), true
}

func formatHost(host string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}

func effectivePort(scheme, port string) (int, error) {
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return 0, errors.New("invalid port")
		}
		return value, nil
	}
	if scheme == "https" {
		return 443, nil
	}
	return 80, nil
}

func requestOrigin(r *http.Request) (string, bool) {
	parsed, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	return normalizeOrigin(parsed.String())
}

func requestHost(r *http.Request) (string, int, bool) {
	raw := strings.TrimSpace(r.Host)
	if raw == "" || len(raw) > 255 || strings.ContainsAny(raw, "\r\n\t") {
		return "", 0, false
	}
	parsed, err := url.Parse("//" + raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return "", 0, false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	port, err := effectivePort(scheme, parsed.Port())
	if err != nil {
		return "", 0, false
	}
	return host, port, true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func utcNow() string { return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339) }

func newToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func digestChange(target, content string) string {
	hash := sha256.Sum256([]byte(target + "\x00" + content))
	return hex.EncodeToString(hash[:])
}

func (s *sessionStore) pruneLocked(now time.Time) {
	for token, item := range s.items {
		if !item.expires.After(now) {
			delete(s.items, token)
		}
	}
}

func (s *sessionStore) create() (string, string, error) {
	token, err := newToken(32)
	if err != nil {
		return "", "", err
	}
	csrf, err := newToken(24)
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if len(s.items) >= s.maxItems {
		return "", "", errors.New("session capacity reached")
	}
	s.items[token] = &session{csrf: csrf, expires: now.Add(sessionTTL), previews: make(map[string]preview)}
	return token, csrf, nil
}

func (s *sessionStore) get(token string) (*session, bool) {
	if token == "" {
		return nil, false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[token]
	if !ok || !item.expires.After(now) {
		delete(s.items, token)
		return nil, false
	}
	item.expires = now.Add(sessionTTL)
	return item, true
}

func (s *sessionStore) remove(token string) {
	s.mu.Lock()
	delete(s.items, token)
	s.mu.Unlock()
}

func (l *loginLimiter) allowed(address string) (bool, time.Duration) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.items) >= l.maxItems {
		for key, item := range l.items {
			if len(item.stamps) == 0 || now.Sub(item.stamps[len(item.stamps)-1]) >= loginWindow {
				delete(l.items, key)
			}
		}
		if len(l.items) >= l.maxItems {
			return false, loginWindow
		}
	}
	item := l.items[address]
	kept := item.stamps[:0]
	for _, stamp := range item.stamps {
		if now.Sub(stamp) < loginWindow {
			kept = append(kept, stamp)
		}
	}
	item.stamps = kept
	if len(item.stamps) >= loginMaxAttempts {
		return false, time.Until(item.stamps[0].Add(loginWindow))
	}
	item.stamps = append(item.stamps, now)
	l.items[address] = item
	return true, 0
}

func (l *loginLimiter) clear(address string) {
	l.mu.Lock()
	delete(l.items, address)
	l.mu.Unlock()
}

func (j *jobStore) pruneLocked() {
	cutoff := time.Now().Add(-jobTTL)
	for id, item := range j.items {
		created, _ := time.Parse(time.RFC3339, item.CreatedAt)
		finished, _ := time.Parse(time.RFC3339, item.FinishedAt)
		when := created
		if !finished.IsZero() {
			when = finished
		}
		if item.Status != "running" && !when.IsZero() && when.Before(cutoff) {
			delete(j.items, id)
		}
	}
}

func (j *jobStore) start(name string, callback func() (map[string]any, error)) (string, bool) {
	j.mu.Lock()
	j.pruneLocked()
	if id := j.active[name]; id != "" {
		if item, ok := j.items[id]; ok && item.Status == "running" {
			j.mu.Unlock()
			return id, false
		}
		delete(j.active, name)
	}
	if len(j.items) >= j.maxItems {
		j.mu.Unlock()
		return "", false
	}
	id, err := newToken(18)
	if err != nil {
		j.mu.Unlock()
		return "", false
	}
	// UUID-shaped IDs preserve the existing frontend route contract.
	id = id[:8] + "-" + id[8:12] + "-4" + id[13:16] + "-8" + id[17:20] + "-" + id[20:32]
	now := utcNow()
	j.items[id] = &job{ID: id, Name: name, Status: "running", CreatedAt: now, StartedAt: now}
	j.active[name] = id
	j.mu.Unlock()
	go j.run(id, name, callback)
	return id, true
}

func (j *jobStore) run(id, name string, callback func() (map[string]any, error)) {
	result, err := callback()
	j.mu.Lock()
	defer j.mu.Unlock()
	item, ok := j.items[id]
	if !ok {
		return
	}
	if err != nil {
		item.Status = "failed"
		item.Error = "operation failed"
	} else {
		item.Status = "succeeded"
		item.Result = safeMap(result)
	}
	item.FinishedAt = utcNow()
	if j.active[name] == id {
		delete(j.active, name)
	}
}

func (j *jobStore) get(id string) (job, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.pruneLocked()
	item, ok := j.items[id]
	if !ok {
		return job{}, false
	}
	copy := *item
	if item.Result != nil {
		copy.Result = safeMap(item.Result)
	}
	return copy, true
}

func (j *jobStore) summary() []map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.pruneLocked()
	result := make([]map[string]any, 0, len(j.items))
	for _, item := range j.items {
		result = append(result, map[string]any{
			"id": item.ID, "name": item.Name, "status": item.Status,
			"created_at": item.CreatedAt, "started_at": item.StartedAt, "finished_at": item.FinishedAt,
		})
	}
	sort.Slice(result, func(i, k int) bool { return fmt.Sprint(result[i]["created_at"]) > fmt.Sprint(result[k]["created_at"]) })
	return result
}

func safeMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	clean, ok := safeValue(value, "", 0).(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return clean
}

func redactText(value string) string {
	if len(value) > maxStringBytes {
		value = value[:maxStringBytes] + "[truncated]"
	}
	value = credentialURL.ReplaceAllString(value, "$1[redacted]@")
	value = bearerValue.ReplaceAllString(value, "$1[redacted]")
	return keyValueSecret.ReplaceAllString(value, "$1[redacted]")
}

func safeValue(value any, key string, depth int) any {
	if depth > 8 {
		return "[truncated]"
	}
	// This is a short-lived browser nonce, not a provider credential. The UI
	// needs it to protect state-changing requests with the Strict cookie.
	if strings.EqualFold(key, "csrf_token") {
		if token, ok := value.(string); ok {
			return redactText(token)
		}
	}
	if key != "" && sensitiveKey.MatchString(key) {
		return "[redacted]"
	}
	switch item := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return item
	case string:
		return redactText(item)
	case []byte:
		return redactText(string(item))
	case map[string]any:
		result := make(map[string]any)
		count := 0
		for childKey, child := range item {
			if count >= 200 {
				result["[truncated]"] = true
				break
			}
			result[childKey] = safeValue(child, childKey, depth+1)
			count++
		}
		return result
	case []any:
		result := make([]any, 0, minInt(len(item), 200))
		for index, child := range item {
			if index >= 200 {
				break
			}
			result = append(result, safeValue(child, "", depth+1))
		}
		return result
	default:
		return safeReflected(value, depth)
	}
}

func safeReflected(value any, depth int) any {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return nil
	}
	switch reflected.Kind() {
	case reflect.Interface, reflect.Pointer:
		if reflected.IsNil() {
			return nil
		}
		return safeReflected(reflected.Elem().Interface(), depth+1)
	case reflect.Map:
		result := make(map[string]any)
		iter := reflected.MapRange()
		count := 0
		for iter.Next() {
			if count >= 200 {
				result["[truncated]"] = true
				break
			}
			key := fmt.Sprint(iter.Key().Interface())
			result[key] = safeValue(iter.Value().Interface(), key, depth+1)
			count++
		}
		return result
	case reflect.Slice, reflect.Array:
		result := make([]any, 0, minInt(reflected.Len(), 200))
		for index := 0; index < reflected.Len() && index < 200; index++ {
			result = append(result, safeValue(reflected.Index(index).Interface(), "", depth+1))
		}
		return result
	default:
		return redactText(fmt.Sprint(value))
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// reveal marks a response whose fields are intentionally exempt from field-name
// redaction. Only one-time credentials generated in this process use it: a
// signing secret has to reach the browser once, and it is never read back from
// the store afterwards.
type reveal struct{ value any }

func jsonBytes(value any) ([]byte, error) {
	if item, ok := value.(reveal); ok {
		value = item.value
	} else {
		value = safeValue(value, "", 0)
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxResponseBytes {
		return nil, errors.New("response too large")
	}
	return encoded, nil
}

func (s *Server) startJob(name string, callback func() (map[string]any, error)) (string, bool) {
	return s.jobs.start(name, callback)
}

// channelBackend reports whether the configured store exposes channels.
func (s *Server) channelBackend() (ChannelBackend, bool) {
	backend, ok := s.store.(ChannelBackend)
	return backend, ok
}

// channelErrorCode maps a backend channel error to a stable client code.
// Anything unrecognized becomes a generic failure so a new backend error
// cannot leak internal wording into the UI.
func channelErrorCode(err error) string {
	if err == nil {
		return "operation_failed"
	}
	message := err.Error()
	switch message {
	case "device_key_required", "channel_not_configured", "invalid_channel", "invalid_level", "invalid_language":
		return message
	default:
		return "operation_failed"
	}
}

// hookBackend reports whether the configured store exposes inbound hooks.
func (s *Server) hookBackend() (HookBackend, bool) {
	backend, ok := s.store.(HookBackend)
	return backend, ok
}

// inboundHookPath matches exactly one hook id: management verbs live one
// segment deeper and still require a browser session.
func inboundHookPath(path string) bool { return inboundHookRE.MatchString(path) }

// hookErrorCode maps store hook errors to stable client codes.
func hookErrorCode(err error) string {
	if err == nil {
		return "hook_invalid"
	}
	switch err.Error() {
	case "invalid_hook_id", "invalid_hook_name", "invalid_hook_action", "hook_not_found", "hook_limit_reached",
		"hook_secret_unavailable":
		return err.Error()
	default:
		return "hook_invalid"
	}
}

// hookEvent accepts one signed inbound event. The body is read raw because the
// signature covers the exact bytes, not a re-encoding of a parsed object.
func (s *Server) hookEvent(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.hookBackend()
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "hook_unavailable")
		return
	}
	if r.ContentLength > maxHookBodyBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, "hook_body_too_large")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBodyBytes+1))
	if err != nil || len(body) > maxHookBodyBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, "hook_body_too_large")
		return
	}
	timestamp, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(hookTimestampHeader)), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "hook_stale_timestamp")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/hooks/")
	action, code := backend.AcceptHook(id, timestamp, strings.TrimSpace(r.Header.Get(hookEventIDHeader)), r.Header.Get(hookSignatureHeader), body)
	switch code {
	case "ok":
	case "duplicate_event":
		s.writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "duplicate": true, "hook": id}, nil)
		return
	case "unknown_hook":
		s.writeError(w, http.StatusNotFound, "hook_unknown")
		return
	case "hook_disabled":
		s.writeError(w, http.StatusForbidden, "hook_disabled")
		return
	case "stale_timestamp":
		s.writeError(w, http.StatusUnauthorized, "hook_stale_timestamp")
		return
	case "invalid_signature":
		s.writeError(w, http.StatusUnauthorized, "hook_invalid_signature")
		return
	case "invalid_event_id":
		s.writeError(w, http.StatusBadRequest, "hook_invalid_event")
		return
	case "rate_limited":
		s.writeError(w, http.StatusTooManyRequests, "hook_rate_limited")
		return
	default:
		s.writeError(w, http.StatusServiceUnavailable, "hook_unavailable")
		return
	}
	if action != "maintenance" {
		// A stored action outside the known set is a configuration error; refuse
		// rather than guess what the caller meant.
		s.writeError(w, http.StatusServiceUnavailable, "hook_action_unsupported")
		return
	}
	jobID, started := s.startJob("automation:codex", func() (map[string]any, error) {
		return s.store.RunAutomation("hook", "", "codex")
	})
	if jobID == "" {
		s.writeError(w, http.StatusServiceUnavailable, "job_unavailable")
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true, "hook": id, "action": action,
		"job_id": jobID, "started": started, "status": jobStatus(started),
	}, nil)
}

// hookAdmin handles the session-authenticated management verbs under
// /api/hooks/{id}/. It reports whether it handled the path.
func (s *Server) hookAdmin(w http.ResponseWriter, r *http.Request, payload map[string]any) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/hooks/"), "/")
	if len(parts) != 2 {
		return false
	}
	id, verb := parts[0], parts[1]
	backend, ok := s.hookBackend()
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "hook_unavailable")
		return true
	}
	var (
		value map[string]any
		err   error
	)
	switch verb {
	case "update":
		enabled, _ := payload["enabled"].(bool)
		name, _ := payload["name"].(string)
		value, err = backend.UpdateHook(id, enabled, name)
	case "rotate":
		value, err = backend.RotateHookSecret(id)
	case "delete":
		value, err = backend.DeleteHook(id)
	default:
		return false
	}
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "hook_not_found" {
			status = http.StatusNotFound
		}
		s.writeError(w, status, hookErrorCode(err))
		return true
	}
	// The rotate response carries the new secret exactly once, so it bypasses
	// the field redaction that would otherwise strip it.
	s.writeJSON(w, http.StatusOK, reveal{value}, nil)
	return true
}

func (s *Server) originAllowed(r *http.Request) bool {
	host, hostPort, ok := requestHost(r)
	if !ok || !containsString(s.opts.TrustedHosts, host) {
		return false
	}
	origin, ok := requestOrigin(r)
	if !ok {
		return false
	}
	if containsString(s.opts.TrustedOrigins, origin) {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")) != host {
		return false
	}
	port, err := effectivePort(parsed.Scheme, parsed.Port())
	return err == nil && port == hostPort
}

func (s *Server) hostAllowed(r *http.Request) bool {
	host, _, ok := requestHost(r)
	return ok && containsString(s.opts.TrustedHosts, host)
}

func parseSessionCookie(r *http.Request) string {
	for _, raw := range r.Cookies() {
		if raw.Name == cookieName && safePreviewID.MatchString(raw.Value) {
			return raw.Value
		}
	}
	return ""
}

func (s *Server) session(r *http.Request) (string, *session, bool) {
	token := parseSessionCookie(r)
	item, ok := s.sessions.get(token)
	if !ok {
		return token, nil, false
	}
	return token, item, true
}

func validTarget(value any) (string, bool) {
	target, ok := value.(string)
	if !ok || target == "" || len([]byte(target)) > 512 || strings.ContainsAny(target, "\\\x00") {
		return "", false
	}
	for _, r := range target {
		if r < 32 || r == 127 {
			return "", false
		}
	}
	clean := filepath.ToSlash(filepath.Clean(target))
	if clean != target || filepath.IsAbs(target) || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", false
	}
	first, _, _ := strings.Cut(clean, "/")
	switch first {
	case "core", "active", "domains", "projects", "decisions", "knowledge", "history", "inbox":
		return clean, true
	default:
		return "", false
	}
}

func validContent(value any) (string, bool) {
	content, ok := value.(string)
	return content, ok && len([]byte(content)) <= maxBodyBytes && !strings.ContainsRune(content, '\x00')
}

func validModel(value any) (string, bool) {
	if value == nil {
		return "", true
	}
	model, ok := value.(string)
	if !ok {
		return "", false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", true
	}
	return model, safeModel.MatchString(model)
}

// validRunner defaults an absent or empty selector to Codex. Both runners
// accept a model; DSH applies it to a temporary headless profile overlay.
func validRunner(payload map[string]any) (string, bool) {
	value, exists := payload["runner"]
	if !exists {
		return "codex", true
	}
	runner, ok := value.(string)
	if !ok {
		return "", false
	}
	switch runner {
	case "", "codex":
		return "codex", true
	case "dsh":
		return "dsh", true
	default:
		return "", false
	}
}

func maintenanceJobName(runner, mode string) string {
	return "maintenance:" + runner + ":" + mode
}

func validMessage(value any) (string, bool) {
	message, ok := value.(string)
	if !ok {
		return "", false
	}
	message = strings.TrimSpace(message)
	if message == "" || len([]byte(message)) > 200 || strings.ContainsRune(message, '\x00') {
		return "", false
	}
	for _, r := range message {
		if r < 32 && r != '\n' && r != '\t' {
			return "", false
		}
	}
	return message, true
}

func validRepository(value any) bool {
	name, ok := value.(string)
	return ok && safeRepositoryPart.MatchString(name)
}

func requestBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	if r.ContentLength > maxBodyBytes {
		return nil, errors.New("request too large")
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		return nil, errors.New("json required")
	}
	limited := io.LimitReader(r.Body, maxBodyBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil || len(raw) > maxBodyBytes {
		return nil, errors.New("request too large")
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, errors.New("invalid json")
	}
	return payload, nil
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any, extra http.Header) {
	encoded, err := jsonBytes(value)
	if err != nil {
		status = http.StatusInternalServerError
		encoded = []byte(`{"error":"response_unavailable"}`)
	}
	for key, values := range extra {
		for _, item := range values {
			w.Header().Add(key, item)
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func (s *Server) writeError(w http.ResponseWriter, status int, code string) {
	s.writeJSON(w, status, map[string]any{"error": code}, nil)
}

func (s *Server) writeStatic(w http.ResponseWriter, name string) {
	allowed := map[string]string{
		"index.html": "text/html; charset=utf-8",
		"app.js":     "text/javascript; charset=utf-8",
		"i18n.js":    "text/javascript; charset=utf-8",
		"styles.css": "text/css; charset=utf-8",
	}
	contentType, ok := allowed[name]
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.opts.AssetsDir, name))
	if err != nil || len(data) > maxResponseBytes {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	address, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil || address == "" {
		address = r.RemoteAddr
	}
	allowed, retry := s.logins.allowed(address)
	if !allowed {
		if retry < time.Second {
			retry = time.Second
		}
		s.writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "login_rate_limited"}, http.Header{"Retry-After": []string{strconv.Itoa(int(retry / time.Second))}})
		return
	}
	password, passwordOK := payload["password"].(string)
	if !passwordOK || !s.auth.verify(password) {
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	s.logins.clear(address)
	token, csrf, err := s.sessions.create()
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "session_unavailable")
		return
	}
	secure := ""
	if s.opts.CookieSecure {
		secure = "; Secure"
	}
	cookie := fmt.Sprintf("%s=%s; Path=/; Max-Age=%d; HttpOnly; SameSite=Strict%s", cookieName, token, int(sessionTTL/time.Second), secure)
	s.writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": csrf}, http.Header{"Set-Cookie": []string{cookie}})
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) (string, *session, bool) {
	token, item, ok := s.session(r)
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "authentication_required")
		return "", nil, false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(item.csrf)) != 1 {
		s.writeError(w, http.StatusForbidden, "csrf_failed")
		return "", nil, false
	}
	return token, item, true
}

func (s *Server) doGET(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if len([]byte(r.URL.RequestURI())) > maxPathBytes || strings.ContainsAny(path, "\\\x00") {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if path == "/" || path == "/index.html" {
		s.writeStatic(w, "index.html")
		return
	}
	if path == "/app.js" || path == "/i18n.js" || path == "/styles.css" {
		s.writeStatic(w, strings.TrimPrefix(path, "/"))
		return
	}
	if path == "/api/session" {
		_, item, ok := s.session(r)
		payload := map[string]any{"authenticated": ok}
		if ok {
			payload["csrf_token"] = item.csrf
		}
		s.writeJSON(w, http.StatusOK, payload, nil)
		return
	}
	if strings.HasPrefix(path, "/api/automation/runs/") {
		_, _, ok := s.session(r)
		if !ok {
			s.writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		runID := strings.TrimPrefix(path, "/api/automation/runs/")
		if !safeReportID.MatchString(runID) {
			s.writeError(w, http.StatusNotFound, "not_found")
			return
		}
		value, err := s.store.AutomationRun(runID)
		if err != nil {
			s.writeError(w, http.StatusNotFound, "not_found")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
		return
	}
	if strings.HasPrefix(path, "/api/jobs/") {
		_, _, ok := s.session(r)
		if !ok {
			s.writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		id := strings.TrimPrefix(path, "/api/jobs/")
		if !safeJobID.MatchString(id) {
			s.writeError(w, http.StatusNotFound, "not_found")
			return
		}
		item, ok := s.jobs.get(id)
		if !ok {
			s.writeError(w, http.StatusNotFound, "not_found")
			return
		}
		s.writeJSON(w, http.StatusOK, item.public(), nil)
		return
	}
	if !strings.HasPrefix(path, "/api/") {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	_, _, ok := s.session(r)
	if !ok && path != "/api/session" {
		s.writeError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	switch path {
	case "/api/state", "/api/status":
		state, err := s.store.State()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "operation_failed")
			return
		}
		state = safeMap(state)
		state["service"] = map[string]any{"scheduler_enabled": s.opts.SchedulerEnabled, "scheduler_interval_seconds": int(s.opts.SchedulerInterval / time.Second), "scheduler_next_at": s.schedulerNextAt(), "jobs": s.jobs.summary()}
		s.writeJSON(w, http.StatusOK, state, nil)
	case "/api/github/repositories":
		value, err := s.store.GitHubRepositories()
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "github_repositories_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/automation":
		value, err := s.store.ListAutomation()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "operation_failed")
			return
		}
		clean := safeMap(value)
		clean["models"] = safeMap(s.store.ModelCatalog())
		if latest, ok := clean["latest"].(map[string]any); ok {
			if id, _ := latest["run_id"].(string); id == "" {
				clean["latest"] = nil
			}
		}
		s.writeJSON(w, http.StatusOK, clean, nil)
	case "/api/hooks":
		backend, ok := s.hookBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "hook_unavailable")
			return
		}
		value, err := backend.ListHooks()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "operation_failed")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/channels":
		backend, ok := s.channelBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "operation_failed")
			return
		}
		value, err := backend.ChannelState()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "operation_failed")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/report":
		id, started := s.startJob(maintenanceJobName("codex", "report"), func() (map[string]any, error) { return s.store.RunMaintenance("report", "", "codex") })
		if id == "" {
			s.writeError(w, http.StatusServiceUnavailable, "job_unavailable")
			return
		}
		s.writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id, "started": started, "status": jobStatus(started)}, nil)
	default:
		s.writeError(w, http.StatusNotFound, "not_found")
	}
}

func jobStatus(started bool) string {
	if started {
		return "queued"
	}
	return "running"
}

func (s *Server) doPOST(w http.ResponseWriter, r *http.Request) {
	// Inbound hook events are authenticated by their signature, not a session,
	// so they are dispatched before the browser payload/session handling.
	if inboundHookPath(r.URL.Path) {
		s.hookEvent(w, r)
		return
	}
	payload, err := requestBody(w, r)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "too large") {
			status = http.StatusRequestEntityTooLarge
		}
		s.writeError(w, status, strings.ReplaceAll(err.Error(), " ", "_"))
		return
	}
	if r.URL.Path == "/api/login" {
		s.login(w, r, payload)
		return
	}
	token, _, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/hooks/") && s.hookAdmin(w, r, payload) {
		return
	}
	switch r.URL.Path {
	case "/api/logout":
		s.sessions.remove(token)
		secure := ""
		if s.opts.CookieSecure {
			secure = "; Secure"
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"authenticated": false}, http.Header{"Set-Cookie": []string{fmt.Sprintf("%s=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict%s", cookieName, secure)}})
	case "/api/github/start":
		value, callErr := s.store.GitHubStart()
		s.writeBackendResult(w, callErr, value)
	case "/api/github/poll":
		value, callErr := s.store.Poll()
		s.writeBackendResult(w, callErr, value)
	case "/api/github/logout":
		value, callErr := s.store.Logout()
		s.writeBackendResult(w, callErr, value)
	case "/api/repository/connect", "/api/connect":
		owner, ownerOK := payload["owner"].(string)
		name, nameOK := payload["name"].(string)
		if !ownerOK || !nameOK || !safeRepositoryPart.MatchString(owner) || !safeRepositoryPart.MatchString(name) {
			s.writeError(w, http.StatusBadRequest, "invalid_repository")
			return
		}
		value, callErr := s.store.ConnectRepository(owner + "/" + name)
		s.writeBackendResult(w, callErr, value)
	case "/api/repository/sync", "/api/sync":
		id, started := s.startJob("repository:sync", func() (map[string]any, error) { return s.store.SyncRepository() })
		if id == "" {
			s.writeError(w, http.StatusServiceUnavailable, "job_unavailable")
			return
		}
		s.writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id, "started": started, "status": jobStatus(started)}, nil)
	case "/api/automation/run":
		runner, runnerOK := validRunner(payload)
		if !runnerOK {
			s.writeError(w, http.StatusBadRequest, "invalid_runner")
			return
		}
		model, modelOK := validModel(payload["model"])
		if !modelOK {
			s.writeError(w, http.StatusBadRequest, "invalid_model")
			return
		}
		id, started := s.startJob("automation:"+runner, func() (map[string]any, error) {
			return s.store.RunAutomation("manual", runner, model)
		})
		if id == "" {
			s.writeError(w, http.StatusServiceUnavailable, "job_unavailable")
			return
		}
		s.writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id, "started": started, "runner": runner, "status": jobStatus(started)}, nil)
	case "/api/maintenance", "/api/maintenance/run":
		mode, modeOK := payload["mode"].(string)
		if !modeOK || mode == "" {
			mode = "report"
		}
		if mode != "report" && mode != "agent" {
			s.writeError(w, http.StatusBadRequest, "invalid_maintenance_mode")
			return
		}
		runner, runnerOK := validRunner(payload)
		if !runnerOK {
			s.writeError(w, http.StatusBadRequest, "invalid_runner")
			return
		}
		model, modelOK := validModel(payload["model"])
		if !modelOK {
			s.writeError(w, http.StatusBadRequest, "invalid_model")
			return
		}
		id, started := s.startJob(maintenanceJobName(runner, mode), func() (map[string]any, error) { return s.store.RunMaintenance(mode, model, runner) })
		if id == "" {
			s.writeError(w, http.StatusServiceUnavailable, "job_unavailable")
			return
		}
		s.writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id, "started": started, "mode": mode, "runner": runner, "status": jobStatus(started)}, nil)
	case "/api/hooks":
		backend, ok := s.hookBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "hook_unavailable")
			return
		}
		name, _ := payload["name"].(string)
		action, _ := payload["action"].(string)
		value, callErr := backend.CreateHook(name, action)
		if callErr != nil {
			s.writeError(w, http.StatusBadRequest, hookErrorCode(callErr))
			return
		}
		// The response carries the signing secret exactly once.
		s.writeJSON(w, http.StatusOK, reveal{value}, nil)
	case "/api/channels/bark":
		backend, ok := s.channelBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "operation_failed")
			return
		}
		value, callErr := backend.SaveBarkChannel(payload)
		if callErr != nil {
			s.writeError(w, http.StatusBadRequest, channelErrorCode(callErr))
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/channels/bark/test":
		backend, ok := s.channelBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "operation_failed")
			return
		}
		value, callErr := backend.TestBarkChannel(payload)
		if callErr != nil {
			s.writeError(w, http.StatusBadRequest, channelErrorCode(callErr))
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	default:
		s.writeError(w, http.StatusNotFound, "not_found")
	}
}

func (s *Server) writeBackendResult(w http.ResponseWriter, err error, value map[string]any) {
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "operation_failed")
		return
	}
	s.writeJSON(w, http.StatusOK, value, nil)
}

// ServeHTTP enforces host/origin checks before dispatching routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !s.hostAllowed(r) {
		s.writeError(w, http.StatusForbidden, "origin_forbidden")
		return
	}
	// Inbound hook callers are scripts, not browsers: they carry no Origin
	// header. They authenticate with an HMAC over the raw body, which a
	// browser cannot produce cross-site, so the origin check does not apply.
	if r.Method == http.MethodPost && !inboundHookPath(r.URL.Path) && !s.originAllowed(r) {
		s.writeError(w, http.StatusForbidden, "origin_forbidden")
		return
	}
	if r.Method == http.MethodGet {
		s.doGET(w, r)
		return
	}
	s.doPOST(w, r)
}

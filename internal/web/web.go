// Package web exposes the authenticated local maintainer HTTP surface.
//
// The package deliberately depends on the small Backend interface only. Git,
// the storage plugin, and maintenance implementation details stay out of it.
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

	"folio/internal/channel/weixin"
	"folio/internal/plugin"
	"folio/internal/quota"
	"folio/internal/storage"
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

	jobTTL      = 24 * time.Hour
	previewTTL  = 10 * time.Minute
	maxJobs     = 256
	maxPreviews = 64

	pbkdf2Rounds = 240_000
	pbkdf2Bytes  = 32
	cookieName   = "folio_session"
)

var (
	safeRepositoryPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	safeModel          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,199}$`)
	safeJobID          = regexp.MustCompile(`^[0-9a-f-]{36}$`)
	safePreviewID      = regexp.MustCompile(`^[A-Za-z0-9_-]{20,100}$`)
	safeReportID       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	safeAgentSession   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	sensitiveKey       = regexp.MustCompile(`(?i)(pass(?:word)?|token|secret|credential|authorization|private[_ -]?key|device[_ -]?(?:code|key)|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|client[_ -]?secret)`)
	credentialURL      = regexp.MustCompile(`(?i)(https?://)([^/\s@]+)@`)
	bearerValue        = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	inboundHookRE      = regexp.MustCompile(`^/api/hooks/[a-z0-9][a-z0-9-]{0,47}$`)
	keyValueSecret     = regexp.MustCompile(`(?i)((?:access[_ -]?token|refresh[_ -]?token|device[_ -]?(?:code|key)|api[_ -]?key|client[_ -]?secret|token|secret|credential|private[_ -]?key|password|authorization)\s*[:=]\s*)[^\s,;]+`)
)

// Backend is the store contract consumed by the HTTP adapter.
type Backend interface {
	State() (map[string]any, error)
	// Executor is the host process boundary handed to built-in plugins.
	Executor() storage.Executor
	ConnectRepository(fullName string) (map[string]any, error)
	SyncRepository() (map[string]any, error)
	RunMaintenance(mode, model, runner string) (map[string]any, error)
	RunAutomation(trigger, runner, model string) (map[string]any, error)
	ListAutomation() (map[string]any, error)
	AutomationRun(runID string) (map[string]any, error)
	UsageSummary(days int) (map[string]any, error)
	ModelCatalog() map[string]any
	SchedulerTick() (map[string]any, error)
}

// ReportResultBackend exposes only bounded report content selected by run ID.
type ReportResultBackend interface {
	ReportResult(runID string) (map[string]any, error)
}

// QuotaBackend is the optional subscription-allowance contract. The source is
// external and optional; a backend without it keeps the core adapter usable.
type QuotaBackend interface {
	Quotas(refresh bool) (quota.Snapshot, error)
}

// WeixinBackend is the optional bidirectional-channel control contract. The
// process supervisor implements it rather than the store: login and polling own
// an exclusive private-directory lock that is not repository state. Every method
// returns or consumes bounded data, never QR payloads, tokens or cursors.
type WeixinBackend interface {
	WeixinStatus() map[string]any
	WeixinStartLogin() (map[string]any, error)
	WeixinLoginStatus() map[string]any
	WeixinSubmitCode(code string) error
	WeixinCancelLogin()
	WeixinConfirm(ownerID string) (map[string]any, error)
	WeixinSetEnabled(enabled bool) (map[string]any, error)
}

// ChannelBackend is the optional outbound-notification contract. It is
// separate from Backend so a backend that has no channels still satisfies the
// core adapter; the handlers answer 503 instead of failing to compile.
type ChannelBackend interface {
	ChannelState() (map[string]any, error)
	SaveBarkChannel(payload map[string]any) (map[string]any, error)
	TestBarkChannel(override map[string]any) (map[string]any, error)
}

// WeixinPushBackend is the optional outbound-WeChat contract. It is separate
// from ChannelBackend so a store without a WeChat transport still satisfies the
// push-channel contract.
type WeixinPushBackend interface {
	SaveWeixinPush(enabled bool) (map[string]any, error)
	TestWeixinPush() (map[string]any, error)
}

// HookBackend is the optional inbound-trigger contract. Like ChannelBackend it
// is separate from Backend so the core adapter stays small.
type HookBackend interface {
	ListHooks() (map[string]any, error)
	CreateHook(name, action, priority string) (map[string]any, error)
	UpdateHook(id string, enabled bool, name, priority string) (map[string]any, error)
	RotateHookSecret(id string) (map[string]any, error)
	DeleteHook(id string) (map[string]any, error)
	AcceptHook(id string, timestamp int64, eventID, signature string, body []byte) (map[string]any, string)
}

// TaskBackend is the optional work-queue contract. Tasks are durable intent;
// runs are attempts. The board reads the former and links to the latter.
type TaskBackend interface {
	TaskKinds() (map[string]any, error)
	ListTasks() (map[string]any, error)
	CreateTask(kind, source, title, priority string, payload map[string]any) (map[string]any, error)
	SetTasksPaused(paused bool) (map[string]any, error)
	UpdateTask(id string, enabled bool) (map[string]any, error)
	RunTaskNow(id string) (map[string]any, error)
	CancelTask(id string) (map[string]any, error)
	DispatchDue() (map[string]any, error)
}

// ActionBackend is the optional runtime-action registry contract. Definitions
// are owner configuration; the Web layer only carries their JSON.
type ActionBackend interface {
	ListActions() (map[string]any, error)
	ValidateActionJSON(raw []byte) error
	PutActionJSON(raw []byte) (map[string]any, error)
	DisableAction(id string) error
	PutActionHostsJSON(raw []byte) (map[string]any, error)
	ActionVersions() (map[string]any, error)
	RestoreActionVersion(name string) error
}

// ActionCatalogBackend is the decoupled, non-executable action surface.
type ActionCatalogBackend interface {
	ActionCatalog() (map[string]any, error)
}

// ActionRunBackend creates durable task intent from a catalog entry.
type ActionRunBackend interface {
	RunAction(id string, fields map[string]any) (map[string]any, error)
}

// FeedbackBackend owns the private feedback inbox, asynchronous intake and the
// owner's clarification answers.
type FeedbackBackend interface {
	CreateFeedbackRequest(values map[string]any, image []byte) (map[string]any, error)
	FeedbackAnswerRequest(id string, values map[string]any) (map[string]any, error)
	ListFeedback() (map[string]any, error)
	FeedbackDetail(id string) (map[string]any, error)
	FeedbackImage(id string) ([]byte, error)
}

// AgentBackend is the optional main-agent contract. The store owns the turn
// loop, tools, sessions and audit; Web only streams bounded events.
type AgentBackend interface {
	AgentAvailable() bool
	AgentRoute(source, typ string) (string, bool, error)
	AgentTurnMap(ctx context.Context, session, text, trust string, emit func(map[string]any)) (map[string]any, error)
	AgentHistory(session string) (map[string]any, error)
}

// AgentConfigBackend is the optional owner configuration surface for the main
// agent. Responses never carry the API key or its file path.
type AgentConfigBackend interface {
	AgentConfig() map[string]any
	PutAgentConfig(payload map[string]any) (map[string]any, error)
}

// ModelServicesBackend manages reusable model connections and task bindings.
// Keys are write-only; listing and testing return bounded public metadata.
type ModelServicesBackend interface {
	ModelServices() (map[string]any, error)
	PutModelServices(payload map[string]any) (map[string]any, error)
	TestModelService(payload map[string]any) (map[string]any, error)
}

// PricingBackend is the optional model-price catalog. The source is external
// and cached; a missing catalog leaves token counts intact and cost unknown.
type PricingBackend interface {
	PricingStatus() map[string]any
	RefreshPricing(ctx context.Context) (map[string]any, error)
}

// Options configures the local web boundary.
type Options struct {
	DataDir string
	// Plugins are the built-in capability plugins compiled into this binary.
	Plugins           *plugin.Registry
	PasswordFile      string
	AssetsDir         string
	TrustedHosts      []string
	TrustedOrigins    []string
	CookieSecure      bool
	SchedulerEnabled  bool
	SchedulerInterval time.Duration
	// Weixin is the optional inbound-channel control surface. A process that
	// wires no supervisor keeps answering 503 for these routes.
	Weixin WeixinBackend
	// DispatcherInterval is how often the task queue is checked. It is
	// independent of the scheduler: deferred work must start even when
	// interval maintenance is disabled.
	DispatcherInterval time.Duration
}

type session struct {
	csrf        string
	expires     time.Time
	userID      int
	username    string
	authVersion int
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
	auth  *authStore

	jobs *jobStore

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
	if opts.DispatcherInterval <= 0 {
		opts.DispatcherInterval = time.Minute
	}
	if opts.DispatcherInterval < 10*time.Second {
		opts.DispatcherInterval = 10 * time.Second
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
	auth, err := openAuthStore(opts.DataDir, opts.PasswordFile)
	if err != nil {
		return nil, err
	}
	return &Server{
		store: store,
		opts:  opts,
		auth:  auth,
		jobs:  &jobStore{items: make(map[string]*job), active: make(map[string]string), maxItems: maxJobs},
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

// StartDispatcher drains the task queue. It runs regardless of
// SchedulerEnabled: a deferred task must still start when interval maintenance
// is off.
func (s *Server) StartDispatcher(ctx context.Context) {
	backend, ok := s.taskBackend()
	if !ok {
		return
	}
	interval := s.opts.DispatcherInterval
	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				_, _ = backend.DispatchDue()
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

func derivePassword(password string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, pbkdf2Rounds, pbkdf2Bytes)
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
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
		// Numbers are counts, not credentials. Without this a token counter
		// would be replaced by "[redacted]" just because its key says token.
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return value
		}
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

func (s *Server) quotaBackend() (QuotaBackend, bool) {
	backend, ok := s.store.(QuotaBackend)
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
	case "device_key_required", "channel_not_configured", "invalid_channel", "invalid_level", "invalid_language", "weixin_unavailable":
		return message
	default:
		return "operation_failed"
	}
}

// weixinCode maps a supervisor error to a stable client code. Anything
// unrecognized collapses to a generic failure so new internal wording cannot
// reach the page.
func weixinCode(err error) string {
	switch {
	case errors.Is(err, weixin.ErrDeploymentOff):
		return "weixin_deployment_off"
	case errors.Is(err, weixin.ErrBusy):
		return "weixin_busy"
	case errors.Is(err, weixin.ErrNotConfigured):
		return "weixin_not_configured"
	case errors.Is(err, weixin.ErrOwnerRequired):
		return "weixin_owner_required"
	case errors.Is(err, weixin.ErrOwnerMismatch):
		return "weixin_owner_mismatch"
	case errors.Is(err, weixin.ErrNoLogin):
		return "weixin_no_login"
	case errors.Is(err, weixin.ErrNoPrompt):
		return "weixin_no_prompt"
	case errors.Is(err, weixin.ErrProtocol):
		return "weixin_invalid_code"
	case errors.Is(err, weixin.ErrState):
		return "weixin_state_error"
	default:
		return "weixin_failed"
	}
}

// weixinStatus keeps conflicts (another login, nothing to confirm) distinct
// from deployment refusals and real faults.
func weixinStatus(err error) int {
	switch weixinCode(err) {
	case "weixin_deployment_off":
		return http.StatusForbidden
	case "weixin_invalid_code":
		return http.StatusBadRequest
	case "weixin_state_error", "weixin_failed":
		return http.StatusInternalServerError
	default:
		return http.StatusConflict
	}
}

// taskBackend reports whether the configured store exposes the task queue.
func (s *Server) taskBackend() (TaskBackend, bool) {
	backend, ok := s.store.(TaskBackend)
	return backend, ok
}

// actionBackend reports whether the configured store exposes runtime actions.
func (s *Server) actionBackend() (ActionBackend, bool) {
	backend, ok := s.store.(ActionBackend)
	return backend, ok
}

// agentBackend reports whether the configured store exposes the main agent.
func (s *Server) agentBackend() (AgentBackend, bool) {
	backend, ok := s.store.(AgentBackend)
	return backend, ok
}

func (s *Server) agentConfigBackend() (AgentConfigBackend, bool) {
	backend, ok := s.store.(AgentConfigBackend)
	return backend, ok
}

func (s *Server) pricingBackend() (PricingBackend, bool) {
	backend, ok := s.store.(PricingBackend)
	return backend, ok
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
	tasks, ok := s.taskBackend()
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "task_unavailable")
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
	event, code := backend.AcceptHook(id, timestamp, strings.TrimSpace(r.Header.Get(hookEventIDHeader)), r.Header.Get(hookSignatureHeader), body)
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
	action := stringField(event, "action")
	name := stringField(event, "name")
	priority := stringField(event, "priority")
	title, payload := hookPayload(body, name)
	created, err := tasks.CreateTask(action, "hook:"+id, title, priority, payload)
	if err != nil {
		if err.Error() == "invalid_task_kind" || err.Error() == "action_disabled" {
			// The hook names a kind that is no longer configured or available.
			// That is an owner configuration error, not a caller error.
			s.writeError(w, http.StatusServiceUnavailable, "hook_action_unsupported")
			return
		}
		s.writeError(w, http.StatusServiceUnavailable, "task_unavailable")
		return
	}
	task, _ := created["task"].(map[string]any)
	taskID := stringField(task, "id")
	// The hook reports success once the work is durable. A task that is not due
	// yet (deferred to the off-peak window) starts when the dispatcher reaches
	// it, so no job is started here.
	jobID := ""
	if taskDueNow(task) {
		jobID, _ = s.startJob("tasks:dispatch", func() (map[string]any, error) {
			return tasks.DispatchDue()
		})
	}
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true, "hook": id, "action": action, "priority": priority,
		"task_id": taskID, "created": created["created"],
		"dispatched": jobID != "", "job_id": jobID,
	}, nil)
}

// taskAdmin handles the session-authenticated task verbs. The queue is owned by
// the store; this layer only maps HTTP to it.
func (s *Server) taskAdmin(w http.ResponseWriter, r *http.Request, payload map[string]any) bool {
	backend, ok := s.taskBackend()
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "task_unavailable")
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	if len(parts) == 1 {
		var (
			value map[string]any
			err   error
		)
		switch parts[0] {
		case "pause":
			paused, _ := payload["paused"].(bool)
			value, err = backend.SetTasksPaused(paused)
		case "dispatch":
			value, err = backend.DispatchDue()
		default:
			return false
		}
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, taskErrorCode(err))
			return true
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
		return true
	}
	if len(parts) != 2 {
		return false
	}
	id, verb := parts[0], parts[1]
	var (
		value map[string]any
		err   error
	)
	switch verb {
	case "update":
		enabled, _ := payload["enabled"].(bool)
		value, err = backend.UpdateTask(id, enabled)
	case "run":
		value, err = backend.RunTaskNow(id)
	case "cancel":
		value, err = backend.CancelTask(id)
	default:
		return false
	}
	if err != nil {
		status := http.StatusBadRequest
		switch err.Error() {
		case "task_not_found":
			status = http.StatusNotFound
		case "task_busy", "task_canceled":
			status = http.StatusConflict
		}
		s.writeError(w, status, taskErrorCode(err))
		return true
	}
	s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	return true
}

// agentStream runs one main-agent turn and forwards bounded events as SSE.
func (s *Server) agentStream(w http.ResponseWriter, r *http.Request, backend AgentBackend, session, text string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "stream_unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	write := func(event map[string]any) {
		raw, err := json.Marshal(safeMap(event))
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}
	if _, err := backend.AgentTurnMap(r.Context(), session, text, "owner", write); err != nil {
		write(map[string]any{"type": "error", "code": err.Error()})
	}
}

// actionAdmin handles the session-authenticated action verbs that carry an id.
func (s *Server) actionAdmin(w http.ResponseWriter, r *http.Request, payload map[string]any) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/actions/"), "/")
	if len(parts) != 2 {
		return false
	}
	if parts[1] == "run" {
		backend, ok := s.store.(ActionRunBackend)
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "action_unavailable")
			return true
		}
		fields := map[string]any{}
		if raw, exists := payload["fields"]; exists {
			values, typed := raw.(map[string]any)
			if !typed {
				s.writeError(w, http.StatusBadRequest, "action_input_invalid")
				return true
			}
			fields = values
		}
		value, err := backend.RunAction(parts[0], fields)
		if err != nil {
			// Only fixed client outcomes stay 4xx; storage, configuration and
			// dispatch failures are internal and must not look like bad input.
			status, code := http.StatusInternalServerError, "action_unavailable"
			switch err.Error() {
			case "action_unknown":
				status, code = http.StatusNotFound, "action_unknown"
			case "action_disabled":
				status, code = http.StatusConflict, "action_disabled"
			case "action_input_invalid":
				status, code = http.StatusBadRequest, "action_input_invalid"
			}
			s.writeError(w, status, code)
			return true
		}
		status := http.StatusOK
		if created, _ := value["created"].(bool); created {
			status = http.StatusCreated
		}
		s.writeJSON(w, status, safeMap(value), nil)
		return true
	}
	if parts[1] != "disable" {
		return false
	}
	backend, ok := s.actionBackend()
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "action_unavailable")
		return true
	}
	if err := backend.DisableAction(parts[0]); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "action_unknown" {
			status = http.StatusNotFound
		}
		s.writeError(w, status, "action_invalid")
		return true
	}
	value, err := backend.ListActions()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "action_unavailable")
		return true
	}
	s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	return true
}

func taskErrorCode(err error) string {
	if err == nil {
		return "task_invalid"
	}
	switch err.Error() {
	case "task_not_found", "task_busy", "task_canceled", "report_request_invalid":
		return err.Error()
	default:
		return "task_invalid"
	}
}

// hookPayload reads an optional title and the data a plugin attached. The body
// is untrusted: it is stored as a bounded flat projection, never executed,
// interpolated into a command, or written to the checkout.
func hookPayload(body []byte, fallback string) (string, map[string]any) {
	var values map[string]any
	if len(body) == 0 || json.Unmarshal(body, &values) != nil {
		return fallback, nil
	}
	title := fallback
	if text, ok := values["title"].(string); ok {
		if trimmed := strings.TrimSpace(text); trimmed != "" && len([]rune(trimmed)) <= 120 {
			title = trimmed
		}
	}
	return title, values
}

// stringField reads a trimmed string without asserting on the concrete type.
func stringField(values map[string]any, key string) string {
	text, _ := values[key].(string)
	return strings.TrimSpace(text)
}

// taskDueNow reports whether the queue may start the task immediately.
func taskDueNow(task map[string]any) bool {
	if task == nil {
		return false
	}
	raw := stringField(task, "not_before")
	if raw == "" {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	return err != nil || !time.Now().Before(at)
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
		priority, _ := payload["priority"].(string)
		value, err = backend.UpdateHook(id, enabled, name, priority)
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
	item, ok := s.auth.GetSession(token)
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
	username, _ := payload["username"].(string)
	password, _ := payload["password"].(string)
	username = strings.TrimSpace(username)
	key := loginAttemptKey(r, username)
	allowed, retry := s.auth.AllowLogin(key)
	if !allowed {
		if retry < time.Second {
			retry = time.Second
		}
		s.writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "login_rate_limited"}, http.Header{"Retry-After": []string{strconv.Itoa(int(retry / time.Second))}})
		return
	}
	setupRequired, setupErr := s.auth.SetupRequired()
	if setupErr != nil {
		s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable")
		return
	}
	if setupRequired {
		s.writeError(w, http.StatusConflict, "setup_required")
		return
	}
	user, ok, err := s.auth.Authenticate(username, password)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable")
		return
	}
	if !ok {
		s.auth.RecordLoginFailure(key)
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	s.auth.ClearLoginFailures(key)
	token, csrf, expires, err := s.auth.CreateSession(user, r.UserAgent())
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "session_unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrf_token":    csrf,
		"username":      user.Username,
		"expires_at":    expires.UTC().Format(time.RFC3339Nano),
	}, http.Header{"Set-Cookie": []string{s.sessionCookie(token)}})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	if !setupAddressAllowed(r) {
		s.writeError(w, http.StatusForbidden, "setup_private_only")
		return
	}
	key := setupAttemptKey(r)
	allowed, retry := s.auth.AllowLogin(key)
	if !allowed {
		if retry < time.Second {
			retry = time.Second
		}
		s.writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "login_rate_limited"}, http.Header{"Retry-After": []string{strconv.Itoa(int(retry / time.Second))}})
		return
	}
	required, err := s.auth.SetupRequired()
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable")
		return
	}
	if !required {
		s.writeError(w, http.StatusConflict, "auth_setup_complete")
		return
	}
	username, _ := payload["username"].(string)
	password, _ := payload["password"].(string)
	username = strings.TrimSpace(username)
	if err := s.auth.CreateUser(username, password); err != nil {
		s.auth.RecordLoginFailure(key)
		switch err.Error() {
		case "auth_username_invalid":
			s.writeError(w, http.StatusBadRequest, "auth_username_invalid")
		case "auth_password_invalid":
			s.writeError(w, http.StatusBadRequest, "auth_password_invalid")
		default:
			s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable")
		}
		return
	}
	s.auth.ClearLoginFailures(key)
	user, ok, err := s.auth.Authenticate(username, password)
	if err != nil || !ok {
		s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable")
		return
	}
	token, csrf, expires, err := s.auth.CreateSession(user, r.UserAgent())
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "session_unavailable")
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"authenticated": true,
		"csrf_token":    csrf,
		"username":      user.Username,
		"expires_at":    expires.UTC().Format(time.RFC3339Nano),
	}, http.Header{"Set-Cookie": []string{s.sessionCookie(token)}})
}

func (s *Server) sessionCookie(token string) string {
	secure := ""
	if s.opts.CookieSecure {
		secure = "; Secure"
	}
	return fmt.Sprintf("%s=%s; Path=/; Max-Age=%d; HttpOnly; SameSite=Strict%s", cookieName, token, int(sessionAbsoluteTTL/time.Second), secure)
}

func (s *Server) clearSessionCookie() string {
	secure := ""
	if s.opts.CookieSecure {
		secure = "; Secure"
	}
	return fmt.Sprintf("%s=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict%s", cookieName, secure)
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
	if path == "/api/plugins" {
		s.pluginList(w)
		return
	}
	if strings.HasPrefix(path, "/api/plugins/") {
		s.pluginAPI(w, r, nil)
		return
	}
	if strings.HasPrefix(path, "/plugins/") {
		s.pluginAsset(w, r)
		return
	}
	if path == "/api/session" {
		_, item, ok := s.session(r)
		setupRequired, setupErr := s.auth.SetupRequired()
		if setupErr != nil {
			s.writeError(w, http.StatusServiceUnavailable, "auth_unavailable")
			return
		}
		payload := map[string]any{"authenticated": ok, "setup_required": setupRequired}
		if ok {
			payload["csrf_token"] = item.csrf
			payload["username"] = item.username
			payload["expires_at"] = item.expires.UTC().Format(time.RFC3339Nano)
		}
		s.writeJSON(w, http.StatusOK, payload, nil)
		return
	}
	if strings.HasPrefix(path, "/api/agent/sessions/") {
		_, _, ok := s.session(r)
		if !ok {
			s.writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		session := strings.TrimPrefix(path, "/api/agent/sessions/")
		if !safeAgentSession.MatchString(session) {
			s.writeError(w, http.StatusNotFound, "not_found")
			return
		}
		backend, ok := s.agentBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "agent_unavailable")
			return
		}
		value, err := backend.AgentHistory(session)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "agent_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
		return
	}
	if strings.HasPrefix(path, "/api/automation/runs/") {
		_, _, ok := s.session(r)
		if !ok {
			s.writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		runID := strings.TrimPrefix(path, "/api/automation/runs/")
		if strings.HasSuffix(runID, "/result") {
			runID = strings.TrimSuffix(runID, "/result")
			backend, supported := s.store.(ReportResultBackend)
			if !supported || !safeReportID.MatchString(runID) {
				s.writeError(w, http.StatusNotFound, "report_result_unavailable")
				return
			}
			value, err := backend.ReportResult(runID)
			if err != nil {
				s.writeError(w, http.StatusNotFound, "report_result_unavailable")
				return
			}
			s.writeJSON(w, http.StatusOK, safeMap(value), nil)
			return
		}
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
	if path == "/api/feedback" || strings.HasPrefix(path, "/api/feedback/") {
		_, _, ok := s.session(r)
		if !ok {
			s.writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		s.feedbackGET(w, r)
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
		state["service"] = map[string]any{"scheduler_enabled": s.opts.SchedulerEnabled, "scheduler_interval_seconds": int(s.opts.SchedulerInterval / time.Second), "scheduler_next_at": s.schedulerNextAt(), "dispatcher_interval_seconds": int(s.opts.DispatcherInterval / time.Second), "jobs": s.jobs.summary()}
		s.writeJSON(w, http.StatusOK, state, nil)
	case "/api/model-services":
		backend, ok := s.store.(ModelServicesBackend)
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "model_services_unavailable")
			return
		}
		value, err := backend.ModelServices()
		s.writeModelServicesResult(w, value, err)
	case "/api/agent/config":
		backend, ok := s.agentConfigBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "agent_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(backend.AgentConfig()), nil)
	case "/api/pricing":
		backend, ok := s.pricingBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "pricing_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, backend.PricingStatus(), nil)
	case "/api/usage":
		days := 30
		if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
			if parsed, convErr := strconv.Atoi(raw); convErr == nil {
				days = parsed
			}
		}
		value, err := s.store.UsageSummary(days)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "usage_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, value, nil)
	case "/api/quotas":
		backend, ok := s.quotaBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "quota_unavailable")
			return
		}
		value, err := backend.Quotas(false)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "quota_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, value.Map(), nil)
	case "/api/weixin":
		if s.opts.Weixin == nil {
			s.writeError(w, http.StatusServiceUnavailable, "weixin_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, s.opts.Weixin.WeixinStatus(), nil)
	case "/api/weixin/login":
		if s.opts.Weixin == nil {
			s.writeError(w, http.StatusServiceUnavailable, "weixin_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, s.opts.Weixin.WeixinLoginStatus(), nil)
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
	case "/api/task-kinds":
		backend, ok := s.taskBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "task_unavailable")
			return
		}
		value, err := backend.TaskKinds()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "task_kinds_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/catalog":
		backend, ok := s.store.(ActionCatalogBackend)
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "action_unavailable")
			return
		}
		value, err := backend.ActionCatalog()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "action_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/actions":
		backend, ok := s.actionBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "action_unavailable")
			return
		}
		value, err := backend.ListActions()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "action_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/actions/versions":
		backend, ok := s.actionBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "action_unavailable")
			return
		}
		value, err := backend.ActionVersions()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "action_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/tasks":
		backend, ok := s.taskBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "task_unavailable")
			return
		}
		value, err := backend.ListTasks()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "task_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
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
	// Multipart feedback must pass session/CSRF checks before body parsing, and
	// must not be mistaken for the JSON request contract used by other routes.
	if r.URL.Path == "/api/feedback" && isMultipartRequest(r) {
		if _, _, ok := s.requireSession(w, r); !ok {
			return
		}
		s.createFeedback(w, r)
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
	if r.URL.Path == "/api/setup" {
		s.setup(w, r, payload)
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
	if strings.HasPrefix(r.URL.Path, "/api/plugins/") {
		s.pluginAPI(w, r, payload)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/feedback/") && strings.HasSuffix(r.URL.Path, "/answers") {
		s.feedbackAnswer(w, r.URL.Path, payload)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/tasks/") && s.taskAdmin(w, r, payload) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/hooks/") && s.hookAdmin(w, r, payload) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/actions/") && s.actionAdmin(w, r, payload) {
		return
	}
	switch r.URL.Path {
	case "/api/feedback":
		s.writeError(w, http.StatusBadRequest, "feedback_invalid")
	case "/api/tasks":
		backend, ok := s.taskBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "task_unavailable")
			return
		}
		kind, kindOK := payload["kind"].(string)
		title, titleOK := payload["title"].(string)
		priority, priorityOK := payload["priority"].(string)
		data, dataOK := payload["payload"].(map[string]any)
		if !kindOK || strings.TrimSpace(kind) == "" || payload["title"] != nil && !titleOK || payload["priority"] != nil && !priorityOK || payload["payload"] != nil && !dataOK {
			s.writeError(w, http.StatusBadRequest, "task_invalid")
			return
		}
		value, err := backend.CreateTask(kind, "manual", title, priority, data)
		if err != nil {
			// Configured actions reuse the catalog's owner-launch validation.
			// Keep those client outcomes distinct from opaque task failures.
			status, code := http.StatusBadRequest, taskErrorCode(err)
			switch err.Error() {
			case "action_disabled":
				status, code = http.StatusConflict, "action_disabled"
			case "action_input_invalid":
				status, code = http.StatusBadRequest, "action_input_invalid"
			}
			s.writeError(w, status, code)
			return
		}
		status := http.StatusOK
		if created, _ := value["created"].(bool); created {
			status = http.StatusCreated
		}
		s.writeJSON(w, status, safeMap(value), nil)
	case "/api/model-services", "/api/model-services/test":
		backend, ok := s.store.(ModelServicesBackend)
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "model_services_unavailable")
			return
		}
		var value map[string]any
		var callErr error
		if r.URL.Path == "/api/model-services/test" {
			value, callErr = backend.TestModelService(payload)
		} else {
			value, callErr = backend.PutModelServices(payload)
		}
		s.writeModelServicesResult(w, value, callErr)
	case "/api/agent/config":
		backend, ok := s.agentConfigBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "agent_unavailable")
			return
		}
		value, callErr := backend.PutAgentConfig(payload)
		if callErr != nil {
			code := callErr.Error()
			switch code {
			case "agent_config_invalid", "agent_key_invalid":
				s.writeError(w, http.StatusBadRequest, code)
			case "agent_config_unavailable", "agent_key_unavailable":
				s.writeError(w, http.StatusInternalServerError, code)
			default:
				s.writeError(w, http.StatusInternalServerError, "agent_config_unavailable")
			}
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/agent/turns":
		backend, ok := s.agentBackend()
		if !ok || !backend.AgentAvailable() {
			s.writeError(w, http.StatusServiceUnavailable, "agent_unavailable")
			return
		}
		target, routed, routeErr := backend.AgentRoute("console", "owner.message")
		if routeErr != nil || !routed || target != "agent:main" {
			s.writeError(w, http.StatusServiceUnavailable, "agent_route_disabled")
			return
		}
		text, _ := payload["text"].(string)
		text = strings.TrimSpace(text)
		if text == "" || len([]byte(text)) > 16*1024 {
			s.writeError(w, http.StatusBadRequest, "agent_input_invalid")
			return
		}
		session, _ := payload["session"].(string)
		if session == "" {
			session = "console-main"
		}
		if !safeAgentSession.MatchString(session) {
			s.writeError(w, http.StatusBadRequest, "agent_session_invalid")
			return
		}
		s.agentStream(w, r, backend, session, text)
	case "/api/actions/validate":
		backend, ok := s.actionBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "action_unavailable")
			return
		}
		raw, err := json.Marshal(payload["action"])
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "action_invalid")
			return
		}
		if err := backend.ValidateActionJSON(raw); err != nil {
			s.writeError(w, http.StatusBadRequest, "action_invalid")
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"valid": true}, nil)
	case "/api/actions":
		backend, ok := s.actionBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "action_unavailable")
			return
		}
		raw, err := json.Marshal(payload["action"])
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "action_invalid")
			return
		}
		value, callErr := backend.PutActionJSON(raw)
		s.writeBackendResult(w, callErr, value)
	case "/api/actions/hosts":
		backend, ok := s.actionBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "action_unavailable")
			return
		}
		raw, err := json.Marshal(map[string]any{"hosts": payload["hosts"]})
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "action_invalid")
			return
		}
		value, callErr := backend.PutActionHostsJSON(raw)
		s.writeBackendResult(w, callErr, value)
	case "/api/actions/restore":
		backend, ok := s.actionBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "action_unavailable")
			return
		}
		name, _ := payload["version"].(string)
		if err := backend.RestoreActionVersion(name); err != nil {
			s.writeError(w, http.StatusBadRequest, "action_invalid")
			return
		}
		value, _ := backend.ListActions()
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/logout":
		s.auth.DeleteSession(token)
		s.writeJSON(w, http.StatusOK, map[string]any{"authenticated": false}, http.Header{"Set-Cookie": []string{s.clearSessionCookie()}})
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
	case "/api/pricing/refresh":
		backend, ok := s.pricingBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "pricing_unavailable")
			return
		}
		value, callErr := backend.RefreshPricing(r.Context())
		if callErr != nil && callErr.Error() == "pricing_refresh_limited" {
			headers := http.Header{}
			if retry, ok := value["retry_after_seconds"].(int); ok && retry > 0 {
				headers.Set("Retry-After", strconv.Itoa(retry))
			}
			value["error"] = "pricing_refresh_limited"
			s.writeJSON(w, http.StatusTooManyRequests, value, headers)
			return
		}
		s.writeJSON(w, http.StatusOK, value, nil)
	case "/api/quotas/refresh":
		backend, ok := s.quotaBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "quota_unavailable")
			return
		}
		value, callErr := backend.Quotas(true)
		if callErr != nil {
			s.writeError(w, http.StatusInternalServerError, "quota_unavailable")
			return
		}
		if value.Error == quota.ErrRefreshLimited {
			headers := http.Header{}
			if value.RetryAfterSeconds > 0 {
				headers.Set("Retry-After", strconv.Itoa(value.RetryAfterSeconds))
			}
			s.writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":               quota.ErrRefreshLimited,
				"retry_after_seconds": value.RetryAfterSeconds,
			}, headers)
			return
		}
		s.writeJSON(w, http.StatusOK, value.Map(), nil)
	case "/api/weixin/login":
		backend := s.opts.Weixin
		if backend == nil {
			s.writeError(w, http.StatusServiceUnavailable, "weixin_unavailable")
			return
		}
		value, callErr := backend.WeixinStartLogin()
		if callErr != nil {
			s.writeError(w, weixinStatus(callErr), weixinCode(callErr))
			return
		}
		s.writeJSON(w, http.StatusAccepted, value, nil)
	case "/api/weixin/login/code":
		backend := s.opts.Weixin
		if backend == nil {
			s.writeError(w, http.StatusServiceUnavailable, "weixin_unavailable")
			return
		}
		code, ok := payload["code"].(string)
		if !ok || len(code) > 32 {
			s.writeError(w, http.StatusBadRequest, "weixin_invalid_code")
			return
		}
		if callErr := backend.WeixinSubmitCode(code); callErr != nil {
			s.writeError(w, weixinStatus(callErr), weixinCode(callErr))
			return
		}
		s.writeJSON(w, http.StatusOK, backend.WeixinLoginStatus(), nil)
	case "/api/weixin/login/cancel":
		backend := s.opts.Weixin
		if backend == nil {
			s.writeError(w, http.StatusServiceUnavailable, "weixin_unavailable")
			return
		}
		backend.WeixinCancelLogin()
		s.writeJSON(w, http.StatusOK, backend.WeixinStatus(), nil)
	case "/api/weixin/confirm":
		backend := s.opts.Weixin
		if backend == nil {
			s.writeError(w, http.StatusServiceUnavailable, "weixin_unavailable")
			return
		}
		owner, ok := payload["owner_id"].(string)
		if !ok || owner == "" || len(owner) > 512 {
			s.writeError(w, http.StatusBadRequest, "weixin_invalid_owner")
			return
		}
		value, callErr := backend.WeixinConfirm(owner)
		if callErr != nil {
			s.writeError(w, weixinStatus(callErr), weixinCode(callErr))
			return
		}
		s.writeJSON(w, http.StatusOK, value, nil)
	case "/api/weixin/enabled":
		backend := s.opts.Weixin
		if backend == nil {
			s.writeError(w, http.StatusServiceUnavailable, "weixin_unavailable")
			return
		}
		enabled, ok := payload["enabled"].(bool)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "weixin_invalid_request")
			return
		}
		value, callErr := backend.WeixinSetEnabled(enabled)
		if callErr != nil {
			s.writeError(w, weixinStatus(callErr), weixinCode(callErr))
			return
		}
		s.writeJSON(w, http.StatusOK, value, nil)
	case "/api/hooks":
		backend, ok := s.hookBackend()
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "hook_unavailable")
			return
		}
		name, _ := payload["name"].(string)
		action, _ := payload["action"].(string)
		priority, _ := payload["priority"].(string)
		value, callErr := backend.CreateHook(name, action, priority)
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
	case "/api/channels/weixin":
		backend, ok := s.store.(WeixinPushBackend)
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "weixin_unavailable")
			return
		}
		enabled, ok := payload["enabled"].(bool)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		value, callErr := backend.SaveWeixinPush(enabled)
		if callErr != nil {
			code := channelErrorCode(callErr)
			status := http.StatusBadRequest
			if code == "weixin_unavailable" {
				status = http.StatusServiceUnavailable
			}
			s.writeError(w, status, code)
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
	case "/api/channels/weixin/test":
		backend, ok := s.store.(WeixinPushBackend)
		if !ok {
			s.writeError(w, http.StatusServiceUnavailable, "weixin_unavailable")
			return
		}
		value, callErr := backend.TestWeixinPush()
		if callErr != nil {
			code := channelErrorCode(callErr)
			status := http.StatusBadRequest
			if code == "weixin_unavailable" {
				status = http.StatusServiceUnavailable
			}
			s.writeError(w, status, code)
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

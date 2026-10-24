package store

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"folio/internal/notify"
)

const (
	maxHooks         = 16
	maxHookNameRunes = 64
	maxHookIDLen     = 48
	hookSecretBytes  = 32

	// hookTimestampSkew bounds how old a signed request may be.
	hookTimestampSkew = 5 * time.Minute
	// hookReplayRetention keeps event ids long enough that any request still
	// inside the skew window is recognized as a replay.
	hookReplayRetention = time.Hour
	maxHookReplayIDs    = 512
	// hookRateWindow and hookRateLimit bound how often one hook may fire, so a
	// misbehaving caller cannot start runs in a loop.
	hookRateWindow = time.Hour
	hookRateLimit  = 20

	maxHookStoreBytes = 64 * 1024
)

var (
	hookIDRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	hookEventRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	hookHexRE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// hookRecord is one configured inbound trigger. The secret is stored because a
// signature cannot be verified against a hash.
type hookRecord struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Action       string `json:"action"`
	Priority     string `json:"priority"`
	Enabled      bool   `json:"enabled"`
	Secret       string `json:"secret"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	LastCalledAt string `json:"last_called_at,omitempty"`
	LastStatus   string `json:"last_status,omitempty"`
	Calls        int    `json:"calls,omitempty"`
}

type hooksFile struct {
	Version int          `json:"version"`
	Hooks   []hookRecord `json:"hooks"`
}

type hookReplayFile struct {
	Seen map[string]string `json:"seen"`
}

// ListHooks returns the masked hook configuration. Secrets are never returned.
func (s *Store) ListHooks() (map[string]any, error) {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	file, err := s.loadHooksLocked()
	if err != nil {
		return nil, err
	}
	return s.hooksPayload(file), nil
}

// CreateHook adds an enabled hook and returns its secret exactly once.
func (s *Store) CreateHook(name, action, priority string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxHookNameRunes || strings.ContainsAny(name, "\r\n\t") {
		return nil, errStore("invalid_hook_name")
	}
	action = strings.TrimSpace(action)
	if action == "" {
		action = taskKindMaintenance
	}
	kind, err := s.taskKindFor(action)
	if err != nil {
		return nil, errStore("invalid_hook_action")
	}
	if priority == "" {
		priority = kindDefaultPriority(kind)
	}
	if !validPriority(priority) {
		return nil, errStore("invalid_hook_priority")
	}

	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	file, err := s.loadHooksLocked()
	if err != nil {
		return nil, err
	}
	if len(file.Hooks) >= maxHooks {
		return nil, errStore("hook_limit_reached")
	}
	secret, err := newHookSecret()
	if err != nil {
		return nil, errStore("hook_secret_unavailable")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	record := hookRecord{
		ID:        s.uniqueHookIDLocked(file, name),
		Name:      name,
		Action:    action,
		Priority:  priority,
		Enabled:   true,
		Secret:    secret,
		CreatedAt: now,
	}
	file.Hooks = append(file.Hooks, record)
	if err := s.saveHooksLocked(file); err != nil {
		return nil, err
	}
	return map[string]any{
		"hook":   publicHook(record),
		"secret": secret,
		"hooks":  s.hooksPayload(file)["hooks"],
	}, nil
}

// UpdateHook enables, disables, or renames one hook.
func (s *Store) UpdateHook(id string, enabled bool, name, priority string) (map[string]any, error) {
	if !hookIDRE.MatchString(id) {
		return nil, errStore("invalid_hook_id")
	}
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	file, err := s.loadHooksLocked()
	if err != nil {
		return nil, err
	}
	index := hookIndex(file, id)
	if index < 0 {
		return nil, errStore("hook_not_found")
	}
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		if len([]rune(trimmed)) > maxHookNameRunes || strings.ContainsAny(trimmed, "\r\n\t") {
			return nil, errStore("invalid_hook_name")
		}
		file.Hooks[index].Name = trimmed
	}
	if priority != "" {
		if !validPriority(priority) {
			return nil, errStore("invalid_hook_priority")
		}
		file.Hooks[index].Priority = priority
	}
	file.Hooks[index].Enabled = enabled
	file.Hooks[index].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.saveHooksLocked(file); err != nil {
		return nil, err
	}
	return s.hooksPayload(file), nil
}

// RotateHookSecret replaces the signing secret and returns it exactly once.
func (s *Store) RotateHookSecret(id string) (map[string]any, error) {
	if !hookIDRE.MatchString(id) {
		return nil, errStore("invalid_hook_id")
	}
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	file, err := s.loadHooksLocked()
	if err != nil {
		return nil, err
	}
	index := hookIndex(file, id)
	if index < 0 {
		return nil, errStore("hook_not_found")
	}
	secret, err := newHookSecret()
	if err != nil {
		return nil, errStore("hook_secret_unavailable")
	}
	file.Hooks[index].Secret = secret
	file.Hooks[index].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.saveHooksLocked(file); err != nil {
		return nil, err
	}
	return map[string]any{
		"hook":   publicHook(file.Hooks[index]),
		"secret": secret,
		"hooks":  s.hooksPayload(file)["hooks"],
	}, nil
}

// DeleteHook removes one hook.
func (s *Store) DeleteHook(id string) (map[string]any, error) {
	if !hookIDRE.MatchString(id) {
		return nil, errStore("invalid_hook_id")
	}
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	file, err := s.loadHooksLocked()
	if err != nil {
		return nil, err
	}
	index := hookIndex(file, id)
	if index < 0 {
		return nil, errStore("hook_not_found")
	}
	file.Hooks = append(file.Hooks[:index], file.Hooks[index+1:]...)
	if err := s.saveHooksLocked(file); err != nil {
		return nil, err
	}
	return s.hooksPayload(file), nil
}

// AcceptHook verifies an inbound request and records the call. The returned
// code is stable and maps directly to an HTTP status. Verification happens
// before any state changes so an unauthenticated caller cannot consume replay
// entries or rate budget.
func (s *Store) AcceptHook(id string, timestamp int64, eventID, signature string, body []byte) (map[string]any, string) {
	if !hookIDRE.MatchString(id) {
		return nil, "unknown_hook"
	}
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	file, err := s.loadHooksLocked()
	if err != nil {
		return nil, "unavailable"
	}
	index := hookIndex(file, id)
	if index < 0 {
		return nil, "unknown_hook"
	}
	record := file.Hooks[index]
	if !record.Enabled {
		return nil, "hook_disabled"
	}
	now := time.Now()
	requested := time.Unix(timestamp, 0)
	if timestamp <= 0 || now.Sub(requested) > hookTimestampSkew || requested.Sub(now) > hookTimestampSkew {
		return nil, "stale_timestamp"
	}
	if !hookEventRE.MatchString(eventID) {
		return nil, "invalid_event_id"
	}
	signature = strings.TrimPrefix(strings.TrimSpace(signature), "sha256=")
	if !hookHexRE.MatchString(strings.ToLower(signature)) {
		return nil, "invalid_signature"
	}
	provided, err := hex.DecodeString(strings.ToLower(signature))
	if err != nil {
		return nil, "invalid_signature"
	}
	if !hmac.Equal(provided, hookSignature(record.Secret, timestamp, body)) {
		return nil, "invalid_signature"
	}
	replay, err := s.loadReplayLocked()
	if err != nil {
		return nil, "unavailable"
	}
	key := id + ":" + eventID
	if _, seen := replay.Seen[key]; seen {
		s.recordHookStatusLocked(file, index, "duplicate", now, false)
		return nil, "duplicate_event"
	}
	if !s.allowHookCall(id, now) {
		s.recordHookStatusLocked(file, index, "rate_limited", now, false)
		return nil, "rate_limited"
	}
	replay.Seen[key] = now.UTC().Format(time.RFC3339Nano)
	s.pruneReplayLocked(replay, now)
	_ = s.saveReplayLocked(replay)
	s.recordHookStatusLocked(file, index, "accepted", now, true)
	return map[string]any{"name": record.Name, "action": record.Action, "priority": record.Priority}, "ok"
}

func (s *Store) recordHookStatusLocked(file *hooksFile, index int, status string, now time.Time, count bool) {
	file.Hooks[index].LastCalledAt = now.UTC().Format(time.RFC3339Nano)
	file.Hooks[index].LastStatus = status
	if count {
		file.Hooks[index].Calls++
	}
	_ = s.saveHooksLocked(file)
}

func (s *Store) allowHookCall(id string, now time.Time) bool {
	if s.hookCalls == nil {
		s.hookCalls = map[string][]time.Time{}
	}
	calls := s.hookCalls[id]
	kept := calls[:0]
	for _, at := range calls {
		if now.Sub(at) <= hookRateWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) >= hookRateLimit {
		s.hookCalls[id] = kept
		return false
	}
	s.hookCalls[id] = append(kept, now)
	return true
}

func hookSignature(secret string, timestamp int64, body []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return mac.Sum(nil)
}

func (s *Store) uniqueHookIDLocked(file *hooksFile, name string) string {
	base := hookSlug(name)
	if base == "" {
		base = "hook"
	}
	for attempt := 0; attempt < 8; attempt++ {
		candidate := base
		if attempt > 0 || hookIndex(file, candidate) >= 0 {
			suffix, err := randomHex(2)
			if err != nil {
				continue
			}
			candidate = notify.ClampRunes(base, maxHookIDLen-5) + "-" + suffix
		}
		if hookIndex(file, candidate) < 0 {
			return candidate
		}
	}
	return notify.ClampRunes(base, maxHookIDLen-5) + "-" + strconv.FormatInt(time.Now().UnixNano()%100000, 10)
}

func hookSlug(name string) string {
	var builder strings.Builder
	pendingDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingDash && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			pendingDash = false
			builder.WriteRune(r)
		default:
			pendingDash = true
		}
		if builder.Len() >= maxHookIDLen {
			break
		}
	}
	return strings.Trim(builder.String(), "-")
}

func hookIndex(file *hooksFile, id string) int {
	for index, record := range file.Hooks {
		if record.ID == id {
			return index
		}
	}
	return -1
}

func publicHook(record hookRecord) map[string]any {
	return map[string]any{
		"id":             record.ID,
		"name":           record.Name,
		"action":         record.Action,
		"priority":       record.Priority,
		"enabled":        record.Enabled,
		"created_at":     record.CreatedAt,
		"updated_at":     record.UpdatedAt,
		"last_called_at": record.LastCalledAt,
		"last_status":    record.LastStatus,
		"calls":          record.Calls,
		"endpoint":       "/api/hooks/" + record.ID,
	}
}

func (s *Store) hooksPayload(file *hooksFile) map[string]any {
	hooks := make([]any, 0, len(file.Hooks))
	for _, record := range file.Hooks {
		hooks = append(hooks, publicHook(record))
	}
	ids, err := s.kindIDs()
	if err != nil {
		ids = []string{taskKindMaintenance}
	}
	actions := make([]any, 0, len(ids))
	for _, id := range ids {
		actions = append(actions, id)
	}
	return map[string]any{
		"hooks":     hooks,
		"actions":   actions,
		"max_hooks": maxHooks,
	}
}

func (s *Store) loadHooksLocked() (*hooksFile, error) {
	if s.hooksCache != nil {
		return s.hooksCache, nil
	}
	file := &hooksFile{Version: 1}
	info, err := os.Lstat(s.hooksPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.hooksCache = file
			return file, nil
		}
		return nil, errStore("hook settings are unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxHookStoreBytes {
		return nil, errStore("hook settings are invalid")
	}
	raw, err := os.ReadFile(s.hooksPath)
	if err != nil || int64(len(raw)) > maxHookStoreBytes {
		return nil, errStore("hook settings are unavailable")
	}
	if err := json.Unmarshal(raw, file); err != nil {
		return nil, errStore("hook settings are invalid")
	}
	for index := range file.Hooks {
		record := &file.Hooks[index]
		if !hookIDRE.MatchString(record.ID) || record.Secret == "" {
			return nil, errStore("hook settings are invalid")
		}
		// Hooks created before priorities existed default to deferred work.
		if record.Priority == "" {
			record.Priority = "P2"
		}
		if !validPriority(record.Priority) {
			record.Priority = "P2"
		}
	}
	s.hooksCache = file
	return file, nil
}

func (s *Store) saveHooksLocked(file *hooksFile) error {
	if err := ensurePrivateDir(filepath.Dir(s.hooksPath)); err != nil {
		return err
	}
	if err := s.savePrivateJSON(s.hooksPath, file); err != nil {
		return err
	}
	s.hooksCache = file
	return nil
}

func (s *Store) loadReplayLocked() (*hookReplayFile, error) {
	if s.hookReplay != nil {
		return s.hookReplay, nil
	}
	replay := &hookReplayFile{Seen: map[string]string{}}
	info, err := os.Lstat(s.replayPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, errStore("hook replay state is unavailable")
		}
		s.hookReplay = replay
		return replay, nil
	}
	if !info.Mode().IsRegular() || info.Size() > maxHookStoreBytes {
		return nil, errStore("hook replay state is invalid")
	}
	raw, err := os.ReadFile(s.replayPath)
	if err != nil {
		return nil, errStore("hook replay state is unavailable")
	}
	if err := json.Unmarshal(raw, replay); err != nil || replay.Seen == nil {
		return nil, errStore("hook replay state is invalid")
	}
	s.hookReplay = replay
	return replay, nil
}

func (s *Store) saveReplayLocked(replay *hookReplayFile) error {
	return s.savePrivateJSON(s.replayPath, replay)
}

func (s *Store) pruneReplayLocked(replay *hookReplayFile, now time.Time) {
	if len(replay.Seen) <= maxHookReplayIDs {
		for key, stamp := range replay.Seen {
			parsed, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil || now.Sub(parsed) > hookReplayRetention {
				delete(replay.Seen, key)
			}
		}
		return
	}
	// Over the cap: keep the most recent entries, which are the ones a replay
	// could still target inside the timestamp window.
	type entry struct {
		key  string
		when time.Time
	}
	entries := make([]entry, 0, len(replay.Seen))
	for key, stamp := range replay.Seen {
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			delete(replay.Seen, key)
			continue
		}
		entries = append(entries, entry{key: key, when: parsed})
	}
	for index := 1; index < len(entries); index++ {
		for back := index; back > 0 && entries[back].when.Before(entries[back-1].when); back-- {
			entries[back], entries[back-1] = entries[back-1], entries[back]
		}
	}
	for index := 0; index < len(entries)-maxHookReplayIDs; index++ {
		delete(replay.Seen, entries[index].key)
	}
}

func newHookSecret() (string, error) {
	raw := make([]byte, hookSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func randomHex(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

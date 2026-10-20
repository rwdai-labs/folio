package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxReportBytes   = 2 * 1024 * 1024
	maxReports       = 40
	maxProposals     = 100
	maxTextRunes     = 16 * 1024
	maxContentBytes  = 1024 * 1024
	maxDiffBytes     = 512 * 1024
	reviewTTL        = 30 * time.Minute
	maxGitOutput     = 8 * 1024 * 1024
	maxJSONState     = 256 * 1024
	maxDevicePayload = 128 * 1024
	githubClientID   = "Iv1.e7b89e013f801f03"
	githubDeviceURL  = "https://github.com/login/device/code"
	githubTokenURL   = "https://github.com/login/oauth/access_token"
)

var (
	repositoryNameRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,99})$`)
	reviewIDRE       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	shaRE            = regexp.MustCompile(`^[0-9a-f]{40}$`)
	branchRE         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	modelRE          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,199}$`)
	reportIDRE       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	deviceCodeRE     = regexp.MustCompile(`^[A-Za-z0-9._~:/_+\-]+$`)
	userCodeRE       = regexp.MustCompile(`(?i)^[A-Z0-9]{4}-[A-Z0-9]{4}$`)
	githubRemoteRE   = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}\.git$`)
)

var (
	canonicalTopLevel = map[string]struct{}{
		"core": {}, "active": {}, "domains": {}, "projects": {},
		"decisions": {}, "knowledge": {}, "history": {}, "inbox": {},
	}
	claimTypes = map[string]struct{}{
		"fact": {}, "preference": {}, "goal": {}, "decision": {},
		"hypothesis": {}, "inference": {},
	}
)

// Store owns only the external runtime state of the maintainer. The cloned
// repository is treated as untrusted input and is never used as a tool root.
type Store struct {
	dataDir         string
	toolRoot        string
	repositoryDir   string
	runtimeDir      string
	runsDir         string
	reviewsDir      string
	pendingDir      string
	reportsDir      string
	codexDir        string
	dshDir          string
	ghConfigDir     string
	homeDir         string
	hooksDir        string
	authPendingPath string
	statePath       string
	lockPath        string

	threadMu sync.Mutex

	authDeviceCode      string
	authDeviceExpiresAt time.Time
	authDeviceInterval  int
	authDeviceLastPoll  time.Time
	authDeviceInfo      map[string]string

	// Tests may use a temporary local bare Git repository. Production callers
	// never set this flag, so normal GitHub remote validation stays strict.
	allowFileRemoteForTests bool
}

// New creates a Store rooted in an external data directory. Both arguments
// are absolute trusted paths after construction; no repository content is
// executed by this package.
func New(dataDir, toolRoot string) (*Store, error) {
	data, err := resolvePath(dataDir)
	if err != nil {
		return nil, errStore("runtime data path is invalid")
	}
	tools, err := resolvePath(toolRoot)
	if err != nil {
		return nil, errStore("tool root path is invalid")
	}
	if data == tools || isWithin(data, tools) || isWithin(tools, data) {
		return nil, errStore("runtime data must be outside the tool root")
	}
	s := &Store{
		dataDir:            data,
		toolRoot:           tools,
		repositoryDir:      filepath.Join(data, "repository"),
		runtimeDir:         filepath.Join(data, "runtime"),
		runsDir:            filepath.Join(data, "runtime", "runs"),
		reviewsDir:         filepath.Join(data, "runtime", "reviews"),
		pendingDir:         filepath.Join(data, "runtime", "pending-reviews"),
		reportsDir:         filepath.Join(data, "runtime", "reports"),
		codexDir:           filepath.Join(data, "codex"),
		dshDir:             filepath.Join(data, "dsh"),
		ghConfigDir:        filepath.Join(data, "secrets", "gh"),
		homeDir:            filepath.Join(data, "home"),
		hooksDir:           filepath.Join(data, "git-hooks"),
		authPendingPath:    filepath.Join(data, "runtime", "github-device.json"),
		statePath:          filepath.Join(data, "state.json"),
		lockPath:           filepath.Join(data, "store.lock"),
		authDeviceInfo:     map[string]string{},
		authDeviceInterval: 5,
	}
	if err := s.ensureDirs(); err != nil {
		return nil, err
	}
	return s, nil
}

// State returns a bounded operational snapshot and never returns credentials.
func (s *Store) State() (map[string]any, error) {
	return s.withLock(func() (map[string]any, error) {
		saved := s.loadState()
		repository := mapValue(saved, "repository")
		auth := mapValue(saved, "github")
		maintenance := mapValue(saved, "maintenance")
		scheduler := mapValue(saved, "scheduler")
		authStatus := publicText(auth["status"], 64)
		if authStatus == "" {
			authStatus = "unknown"
		}
		return map[string]any{
			"version": 1,
			"github": map[string]any{
				"status":        authStatus,
				"authenticated": authStatus == "authenticated",
			},
			"repository": map[string]any{
				"connected": s.repositoryConnected(repository),
				"full_name": publicText(repository["full_name"], 201),
				"branch":    publicText(repository["branch"], 201),
				"head":      validSHA(repository["head"]),
			},
			"model": map[string]any{
				"available":        s.modelAvailable(),
				"provider_runtime": s.codexDir,
			},
			"runners": map[string]any{
				"codex": map[string]any{"available": s.runnerAvailable("codex")},
				"dsh":   map[string]any{"available": s.runnerAvailable("dsh")},
			},
			"maintenance": map[string]any{
				"status":      publicText(maintenance["status"], 64),
				"mode":        publicText(maintenance["mode"], 32),
				"finished_at": publicText(maintenance["finished_at"], 64),
			},
			"scheduler": map[string]any{
				"status":      publicText(scheduler["status"], 64),
				"finished_at": publicText(scheduler["finished_at"], 64),
				"last_run_id": publicText(scheduler["last_run_id"], 64),
				"trigger":     publicText(scheduler["trigger"], 16),
			},
		}, nil
	})
}

func (s *Store) withLock(fn func() (map[string]any, error)) (result map[string]any, err error) {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	if err := s.ensureDirs(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errStore("runtime lock is unavailable")
	}
	defer file.Close()
	if err := lockFile(file); err != nil {
		return nil, errStore("runtime lock is unavailable")
	}
	defer func() {
		if unlockErr := unlockFile(file); err == nil && unlockErr != nil {
			err = errStore("runtime lock is unavailable")
		}
	}()
	return fn()
}

func errStore(message string) error { return errors.New(message) }

func resolvePath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" || strings.IndexByte(raw, 0) >= 0 {
		return "", errStore("path is invalid")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", errStore("path is invalid")
	}
	abs = filepath.Clean(abs)
	// Resolve the nearest existing ancestor so a symlinked parent cannot
	// bypass the data/tool-root separation check.
	suffix := []string{}
	probe := abs
	for {
		_, statErr := os.Lstat(probe)
		if statErr == nil {
			resolved, evalErr := filepath.EvalSymlinks(probe)
			if evalErr != nil {
				return "", errStore("path is invalid")
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return abs, nil
		}
		suffix = append(suffix, filepath.Base(probe))
		probe = parent
	}
}

func isWithin(candidate, root string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

func (s *Store) ensureDirs() error {
	paths := []string{
		s.dataDir, s.runtimeDir, s.runsDir, s.reviewsDir, s.pendingDir, s.reportsDir,
		s.codexDir, s.dshDir, s.ghConfigDir, s.homeDir, s.hooksDir,
	}
	for _, dir := range paths {
		if err := ensurePrivateDir(dir); err != nil {
			return errStore("runtime paths are unavailable")
		}
	}
	return nil
}

func ensurePrivateDir(dir string) error {
	if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errStore("runtime paths must not be symlinks")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errStore("runtime paths are unavailable")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errStore("runtime paths are unavailable")
	}
	_ = os.Chmod(dir, 0o700)
	return nil
}

func mapValue(value map[string]any, key string) map[string]any {
	if nested, ok := value[key].(map[string]any); ok {
		return nested
	}
	return map[string]any{}
}

func (s *Store) loadState() map[string]any {
	value, err := s.loadJSON(s.statePath, maxJSONState)
	if err != nil {
		return map[string]any{}
	}
	data, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return data
}

func (s *Store) saveState(data map[string]any) error {
	return s.savePrivateJSON(s.statePath, data)
}

func (s *Store) loadJSON(file string, maxBytes int64) (any, error) {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBytes {
		return nil, errStore("stored runtime data is invalid")
	}
	raw, err := os.ReadFile(file)
	if err != nil || int64(len(raw)) > maxBytes {
		return nil, errStore("stored runtime data is invalid")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errStore("stored runtime data is invalid")
	}
	return value, nil
}

func (s *Store) savePrivateJSON(file string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return errStore("runtime data could not be encoded")
	}
	raw = append(raw, '\n')
	if err := writeAtomic(file, raw, 0o600); err != nil {
		return errStore("runtime data could not be stored")
	}
	return nil
}

func writeAtomic(file string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(file)
	tmp, err := os.CreateTemp(dir, ".runtime-*")
	if err != nil {
		return errStore("runtime data could not be stored")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return errStore("runtime data could not be stored")
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errStore("runtime data could not be stored")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errStore("runtime data could not be stored")
	}
	if err := tmp.Close(); err != nil {
		return errStore("runtime data could not be stored")
	}
	if err := os.Rename(tmpName, file); err != nil {
		return errStore("runtime data could not be stored")
	}
	_ = os.Chmod(file, mode)
	return nil
}

func (s *Store) setGitHubStatus(status string) error {
	saved := s.loadState()
	saved["github"] = map[string]any{"status": status, "updated_at": nowString()}
	return s.saveState(saved)
}

func (s *Store) recordRepositoryHead(head, branch string) error {
	saved := s.loadState()
	repository := mapValue(saved, "repository")
	repository["head"] = head
	repository["branch"] = branch
	saved["repository"] = repository
	return s.saveState(saved)
}

// configuredRemote returns the remote recorded when the checkout was
// connected. Older state files may not contain it, so derive it from the
// legacy full_name field when possible and let verifyRemote enforce the
// production GitHub URL contract otherwise.
func (s *Store) configuredRemote() string {
	saved := s.loadState()
	repository := mapValue(saved, "repository")
	if remote, ok := repository["remote"].(string); ok && strings.TrimSpace(remote) != "" {
		return strings.TrimSpace(remote)
	}
	fullName, ok := repository["full_name"].(string)
	if !ok {
		return ""
	}
	owner, name, err := s.validateFullName(fullName)
	if err != nil {
		return ""
	}
	return "https://github.com/" + owner + "/" + name + ".git"
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func publicText(value any, limit int) string {
	text, ok := value.(string)
	if !ok || text == "" {
		if value == nil {
			return ""
		}
		text = fmt.Sprint(value)
	}
	var builder strings.Builder
	count := 0
	for _, r := range text {
		if count >= limit {
			break
		}
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			r = '?'
		}
		builder.WriteRune(r)
		count++
	}
	return builder.String()
}

func validSHA(value any) any {
	text, ok := value.(string)
	if ok && shaRE.MatchString(text) {
		return text
	}
	return nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *Store) repositoryConnected(repository map[string]any) bool {
	fullName, ok := repository["full_name"].(string)
	if !ok || fullName == "" {
		return false
	}
	info, err := os.Lstat(s.repositoryDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	gitInfo, err := os.Lstat(filepath.Join(s.repositoryDir, ".git"))
	return err == nil && (gitInfo.IsDir() || gitInfo.Mode().IsRegular()) && gitInfo.Mode()&os.ModeSymlink == 0
}

func (s *Store) modelAvailable() bool {
	return s.runnerAvailable("codex")
}

// runnerAvailable reports whether the selected review runner CLI is installed.
// The scheduler and model object keep using the Codex result for compatibility.
func (s *Store) runnerAvailable(runner string) bool {
	switch runner {
	case "", "codex":
		_, err := exec.LookPath("codex")
		return err == nil
	case "dsh":
		_, err := exec.LookPath("dsh")
		return err == nil
	default:
		return false
	}
}

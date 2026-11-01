package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	authDatabaseName     = "folio.db"
	sessionIdleTTL       = 30 * 24 * time.Hour
	sessionAbsoluteTTL   = 90 * 24 * time.Hour
	minPasswordBytes     = 12
	maxPasswordBytes     = 200
	maxLoginFailures     = 5
	loginFailureWindow   = 15 * time.Minute
	maxSessionRows       = 1024
	maxDeviceLabelRunes  = 120
	maxAuthUsernameRunes = 64
)

var authUsernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type authUser struct {
	ID          int
	Username    string
	Salt        []byte
	Digest      []byte
	AuthVersion int
}

type authStoreError string

func (e authStoreError) Error() string { return string(e) }

func newAuthError(code string) error { return authStoreError(code) }

// authStore owns the local owner identity and durable login sessions. It is
// deliberately separate from the repository/task locks.
type authStore struct {
	db   *sql.DB
	path string
}

func openAuthStore(dataDir, passwordFile string) (*authStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, newAuthError("auth_data_unavailable")
	}
	path := filepath.Join(dataDir, authDatabaseName)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, newAuthError("auth_data_unavailable")
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, newAuthError("auth_data_unavailable")
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, newAuthError("auth_data_unavailable")
	}
	store := &authStore{db: db, path: path}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	if err := store.bootstrapLegacy(ctx, passwordFile); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (a *authStore) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	return a.db.Close()
}

func (a *authStore) migrate(ctx context.Context) error {
	schema := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			username TEXT NOT NULL UNIQUE,
			salt BLOB NOT NULL,
			digest BLOB NOT NULL,
			auth_version INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			csrf TEXT NOT NULL,
			device TEXT NOT NULL,
			created_at TEXT NOT NULL,
			last_seen TEXT NOT NULL,
			idle_expires TEXT NOT NULL,
			absolute_expires TEXT NOT NULL,
			auth_version INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions(user_id)`,
		`CREATE TABLE IF NOT EXISTS login_attempts (
			key TEXT PRIMARY KEY,
			failures INTEGER NOT NULL,
			first_at TEXT NOT NULL,
			last_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`,
		`INSERT OR IGNORE INTO schema_migrations(version) VALUES (1)`,
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return newAuthError("auth_data_unavailable")
	}
	defer tx.Rollback()
	for _, statement := range schema {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return newAuthError("auth_data_unavailable")
		}
	}
	if err := tx.Commit(); err != nil {
		return newAuthError("auth_data_unavailable")
	}
	return nil
}

// bootstrapLegacy imports an existing password hash or plaintext bootstrap file
// exactly once. A user-created account is never overwritten.
func (a *authStore) bootstrapLegacy(ctx context.Context, passwordFile string) error {
	count, err := a.UserCount()
	if err != nil || count > 0 {
		return err
	}
	hashPath := filepath.Join(a.dataDir(), "secrets", "admin-password.hash")
	salt, digest, hashOK := readLegacyHash(hashPath)
	var plaintext string
	if strings.TrimSpace(passwordFile) != "" {
		info, statErr := os.Lstat(passwordFile)
		if statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() <= 4096 {
			if raw, readErr := os.ReadFile(passwordFile); readErr == nil {
				plaintext = strings.TrimSpace(string(raw))
			}
		}
	}
	// The plaintext file is authoritative when both artifacts exist: a cached
	// hash may predate an operator-edited password.
	if plaintext != "" && len(plaintext) <= maxPasswordBytes {
		if hashOK {
			if candidate, deriveErr := derivePassword(plaintext, salt); deriveErr == nil && subtle.ConstantTimeCompare(candidate, digest) == 1 {
				if err := a.insertLegacyUser(ctx, "admin", salt, digest); err != nil {
					return err
				}
				removeIfPresent(passwordFile)
				removeIfPresent(hashPath)
				return nil
			}
		}
		salt = make([]byte, 16)
		if _, randErr := rand.Read(salt); randErr != nil {
			return newAuthError("auth_password_unavailable")
		}
		digest, deriveErr := derivePassword(plaintext, salt)
		if deriveErr != nil {
			return newAuthError("auth_password_unavailable")
		}
		if err := a.insertLegacyUser(ctx, "admin", salt, digest); err != nil {
			return err
		}
		removeIfPresent(passwordFile)
		removeIfPresent(hashPath)
		return nil
	}
	if hashOK {
		if err := a.insertLegacyUser(ctx, "admin", salt, digest); err != nil {
			return err
		}
		removeIfPresent(hashPath)
	}
	return nil
}

func (a *authStore) dataDir() string { return filepath.Dir(a.path) }

func readLegacyHash(path string) ([]byte, []byte, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4096 {
		return nil, nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false
	}
	var saved struct {
		Salt   string `json:"salt"`
		Digest string `json:"digest"`
	}
	if json.Unmarshal(raw, &saved) != nil {
		return nil, nil, false
	}
	salt, _ := hex.DecodeString(saved.Salt)
	digest, _ := hex.DecodeString(saved.Digest)
	if len(salt) < 16 || len(digest) != pbkdf2Bytes {
		return nil, nil, false
	}
	return salt, digest, true
}

func (a *authStore) insertLegacyUser(ctx context.Context, username string, salt, digest []byte) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(ctx, `INSERT INTO users(id, username, salt, digest, auth_version, created_at, updated_at)
		VALUES (1, ?, ?, ?, 1, ?, ?)`, username, salt, digest, now, now)
	if err != nil {
		return newAuthError("auth_user_conflict")
	}
	return nil
}

func removeIfPresent(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	_ = os.Remove(path)
}

func (a *authStore) UserCount() (int, error) {
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return 0, newAuthError("auth_unavailable")
	}
	return count, nil
}

func (a *authStore) SetupRequired() (bool, error) {
	count, err := a.UserCount()
	return count == 0, err
}

func validAuthUsername(username string) bool {
	return authUsernameRE.MatchString(username) && len([]rune(username)) <= maxAuthUsernameRunes
}

func validAuthPassword(password string) bool {
	if len(password) < minPasswordBytes || len(password) > maxPasswordBytes {
		return false
	}
	return !strings.ContainsRune(password, 0)
}

// CreateUser creates the single owner account. It fails closed once a user
// exists; there is no registration endpoint after setup.
func (a *authStore) CreateUser(username, password string) error {
	username = strings.TrimSpace(username)
	if !validAuthUsername(username) {
		return newAuthError("auth_username_invalid")
	}
	if !validAuthPassword(password) {
		return newAuthError("auth_password_invalid")
	}
	count, err := a.UserCount()
	if err != nil {
		return err
	}
	if count > 0 {
		return newAuthError("auth_setup_complete")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return newAuthError("auth_password_unavailable")
	}
	digest, err := derivePassword(password, salt)
	if err != nil {
		return newAuthError("auth_password_unavailable")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = a.db.Exec(`INSERT INTO users(id, username, salt, digest, auth_version, created_at, updated_at)
		VALUES (1, ?, ?, ?, 1, ?, ?)`, username, salt, digest, now, now)
	if err != nil {
		return newAuthError("auth_user_conflict")
	}
	return nil
}

func (a *authStore) Authenticate(username, password string) (authUser, bool, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(password) > maxPasswordBytes {
		return authUser{}, false, nil
	}
	var user authUser
	err := a.db.QueryRow(`SELECT id, username, salt, digest, auth_version FROM users WHERE username = ?`, username).
		Scan(&user.ID, &user.Username, &user.Salt, &user.Digest, &user.AuthVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return authUser{}, false, nil
	}
	if err != nil {
		return authUser{}, false, newAuthError("auth_unavailable")
	}
	candidate, err := derivePassword(password, user.Salt)
	if err != nil {
		return authUser{}, false, newAuthError("auth_password_unavailable")
	}
	if subtle.ConstantTimeCompare(candidate, user.Digest) != 1 {
		return authUser{}, false, nil
	}
	return user, true, nil
}

func (a *authStore) ChangePassword(username, current, next string) error {
	user, ok, err := a.Authenticate(username, current)
	if err != nil {
		return err
	}
	if !ok {
		return newAuthError("auth_invalid_credentials")
	}
	if !validAuthPassword(next) {
		return newAuthError("auth_password_invalid")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return newAuthError("auth_password_unavailable")
	}
	digest, err := derivePassword(next, salt)
	if err != nil {
		return newAuthError("auth_password_unavailable")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.db.Exec(`UPDATE users SET salt = ?, digest = ?, auth_version = auth_version + 1, updated_at = ? WHERE id = ?`, salt, digest, now, user.ID); err != nil {
		return newAuthError("auth_unavailable")
	}
	_, _ = a.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, user.ID)
	return nil
}

func authTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a *authStore) CreateSession(user authUser, device string) (string, string, time.Time, error) {
	token, err := newToken(32)
	if err != nil {
		return "", "", time.Time{}, newAuthError("auth_session_unavailable")
	}
	csrf, err := newToken(24)
	if err != nil {
		return "", "", time.Time{}, newAuthError("auth_session_unavailable")
	}
	device = strings.TrimSpace(device)
	if device == "" {
		device = "unknown"
	}
	device = clampAuthText(device, maxDeviceLabelRunes)
	now := time.Now().UTC()
	idle := now.Add(sessionIdleTTL)
	absolute := now.Add(sessionAbsoluteTTL)
	_, _ = a.db.Exec(`DELETE FROM sessions WHERE idle_expires <= ? OR absolute_expires <= ?`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	var count int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count)
	if count >= maxSessionRows {
		_, _ = a.db.Exec(`DELETE FROM sessions WHERE token_hash IN (SELECT token_hash FROM sessions ORDER BY last_seen ASC LIMIT ?)`, count-maxSessionRows+1)
	}
	_, err = a.db.Exec(`INSERT INTO sessions(token_hash, user_id, csrf, device, created_at, last_seen, idle_expires, absolute_expires, auth_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		authTokenHash(token), user.ID, csrf, device, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		idle.Format(time.RFC3339Nano), absolute.Format(time.RFC3339Nano), user.AuthVersion)
	if err != nil {
		return "", "", time.Time{}, newAuthError("auth_session_unavailable")
	}
	return token, csrf, idle, nil
}

func (a *authStore) GetSession(token string) (*session, bool) {
	if token == "" || len(token) > 256 {
		return nil, false
	}
	hash := authTokenHash(token)
	var (
		userID      int
		csrf        string
		createdAt   string
		lastSeen    string
		idleExpires string
		absolute    string
		authVersion int
		username    string
		userVersion int
	)
	err := a.db.QueryRow(`SELECT s.user_id, s.csrf, s.created_at, s.last_seen, s.idle_expires,
		s.absolute_expires, s.auth_version, u.username, u.auth_version
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, hash).
		Scan(&userID, &csrf, &createdAt, &lastSeen, &idleExpires, &absolute, &authVersion, &username, &userVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	now := time.Now().UTC()
	idle, idleErr := time.Parse(time.RFC3339Nano, idleExpires)
	absoluteTime, absoluteErr := time.Parse(time.RFC3339Nano, absolute)
	if idleErr != nil || absoluteErr != nil || !now.Before(idle) || !now.Before(absoluteTime) || authVersion != userVersion {
		_, _ = a.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hash)
		return nil, false
	}
	nextIdle := now.Add(sessionIdleTTL)
	if nextIdle.After(absoluteTime) {
		nextIdle = absoluteTime
	}
	_, _ = a.db.Exec(`UPDATE sessions SET last_seen = ?, idle_expires = ? WHERE token_hash = ?`,
		now.Format(time.RFC3339Nano), nextIdle.Format(time.RFC3339Nano), hash)
	return &session{csrf: csrf, expires: nextIdle, userID: userID, username: username, authVersion: authVersion}, true
}

func (a *authStore) DeleteSession(token string) {
	if token == "" {
		return
	}
	_, _ = a.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, authTokenHash(token))
}

func (a *authStore) DeleteAllSessions(userID int) {
	if userID <= 0 {
		return
	}
	_, _ = a.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
}

func (a *authStore) AllowLogin(key string) (bool, time.Duration) {
	now := time.Now().UTC()
	var failures int
	var firstAt string
	err := a.db.QueryRow(`SELECT failures, first_at FROM login_attempts WHERE key = ?`, key).Scan(&failures, &firstAt)
	if errors.Is(err, sql.ErrNoRows) {
		return true, 0
	}
	if err != nil {
		return true, 0
	}
	first, parseErr := time.Parse(time.RFC3339Nano, firstAt)
	if parseErr != nil || now.Sub(first) >= loginFailureWindow {
		_, _ = a.db.Exec(`DELETE FROM login_attempts WHERE key = ?`, key)
		return true, 0
	}
	if failures < maxLoginFailures {
		return true, 0
	}
	retry := loginFailureWindow - now.Sub(first)
	if retry < time.Second {
		retry = time.Second
	}
	return false, retry
}

func (a *authStore) RecordLoginFailure(key string) {
	now := time.Now().UTC()
	var failures int
	var firstAt string
	err := a.db.QueryRow(`SELECT failures, first_at FROM login_attempts WHERE key = ?`, key).Scan(&failures, &firstAt)
	first, parseErr := time.Parse(time.RFC3339Nano, firstAt)
	if errors.Is(err, sql.ErrNoRows) || parseErr != nil || now.Sub(first) >= loginFailureWindow {
		_, _ = a.db.Exec(`INSERT INTO login_attempts(key, failures, first_at, last_at) VALUES (?, 1, ?, ?)
			ON CONFLICT(key) DO UPDATE SET failures = 1, first_at = excluded.first_at, last_at = excluded.last_at`,
			key, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		return
	}
	_, _ = a.db.Exec(`UPDATE login_attempts SET failures = failures + 1, last_at = ? WHERE key = ?`,
		now.Format(time.RFC3339Nano), key)
}

func (a *authStore) ClearLoginFailures(key string) {
	_, _ = a.db.Exec(`DELETE FROM login_attempts WHERE key = ?`, key)
}

func clampAuthText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func authClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if host == "" {
		return "unknown"
	}
	return host
}

func setupAddressAllowed(r *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return false
}

func loginAttemptKey(r *http.Request, username string) string {
	return "login:" + authClientIP(r) + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

func setupAttemptKey(r *http.Request) string {
	return "setup:" + authClientIP(r)
}

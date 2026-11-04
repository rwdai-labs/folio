package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"time"

	"folio/internal/storage"
)

// refreshTokenRE is the shape GitHub uses for refresh tokens issued by the
// device flow. An unrecognized value must never replace a working credential.
var refreshTokenRE = regexp.MustCompile(`^ghr_[A-Za-z0-9]{16,300}$`)

const (
	// credentialRefreshWindow renews slightly before expiry so a request that
	// starts with a valid token cannot lose its credential while running.
	credentialRefreshWindow = 2 * time.Minute
	maxCredentialLifetime   = 400 * 24 * time.Hour
	maxCredentialBytes      = 16 * 1024
)

// errCredentialRejected means the refresh token is no longer accepted and only
// a new device sign-in can restore access.
var errCredentialRejected = errors.New("GitHub sign-in is no longer valid")

// oauthCredential is the refreshable half of a device-flow sign-in. The access
// token itself lives in gh's own credential store; this file carries what gh
// cannot rotate on its own. GitHub issues expiring access tokens (eight hours)
// with a rotating refresh token (six months) to OAuth apps that opt in, and
// new apps opt in by default. Without this file the token would simply expire.
type oauthCredential struct {
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_token_expires_at,omitempty"`
}

func (c *oauthCredential) refreshable() bool {
	return c != nil && c.RefreshToken != "" && !c.AccessExpiresAt.IsZero()
}

func (p *Plugin) loadCredential() (*oauthCredential, bool) {
	raw, err := os.ReadFile(p.oauthPath)
	if err != nil || len(raw) == 0 || len(raw) > maxCredentialBytes {
		return nil, false
	}
	var cred oauthCredential
	if json.Unmarshal(raw, &cred) != nil || !cred.refreshable() || !refreshTokenRE.MatchString(cred.RefreshToken) {
		return nil, false
	}
	return &cred, true
}

// saveCredential replaces the refresh state in one rename so a crash can never
// leave a partially written refresh token behind.
func (p *Plugin) saveCredential(cred *oauthCredential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return errors.New("GitHub credential file is unavailable")
	}
	temp, err := os.CreateTemp(p.secretsDir, ".github-credential-")
	if err != nil {
		return errors.New("GitHub credential file is unavailable")
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return errors.New("GitHub credential file is unavailable")
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return errors.New("GitHub credential file is unavailable")
	}
	if err := temp.Close(); err != nil {
		return errors.New("GitHub credential file is unavailable")
	}
	if err := os.Rename(name, p.oauthPath); err != nil {
		return errors.New("GitHub credential file is unavailable")
	}
	return nil
}

func (p *Plugin) clearCredential() {
	_ = os.Remove(p.oauthPath)
	p.invalidateStatus()
}

// credentialFromResponse extracts a refreshable credential from a device-flow
// or refresh token response. A response without a recognized refresh token is
// a long-lived credential and is reported as absent, not as an error.
func credentialFromResponse(result map[string]any) (*oauthCredential, bool) {
	refresh := stringValue(result["refresh_token"])
	expiresAt, ok := credentialExpiry(result["expires_in"])
	if !refreshTokenRE.MatchString(refresh) || !ok {
		return nil, false
	}
	cred := &oauthCredential{RefreshToken: refresh, AccessExpiresAt: expiresAt}
	if refreshExpiresAt, ok := credentialExpiry(result["refresh_token_expires_in"]); ok {
		cred.RefreshExpiresAt = refreshExpiresAt
	}
	return cred, true
}

// credentialExpiry converts a token-lifetime field to an absolute time. It is
// separate from intValue because a refresh token's lifetime exceeds its bound.
func credentialExpiry(value any) (time.Time, bool) {
	seconds, ok := secondsValue(value)
	if !ok || seconds < 60 || time.Duration(seconds)*time.Second > maxCredentialLifetime {
		return time.Time{}, false
	}
	return time.Now().Add(time.Duration(seconds) * time.Second), true
}

func secondsValue(value any) (int64, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number > 1e9 || number != float64(int64(number)) {
		return 0, false
	}
	return int64(number), true
}

// ensureCredential renews an expiring OAuth access token before the caller
// uses it. A deployment token needs no renewal, and a long-lived sign-in has
// no refresh state, so both are no-ops here.
func (p *Plugin) ensureCredential(ctx context.Context, exec storage.Executor) error {
	if p.tokenFromEnv() != "" {
		return nil
	}
	cred, ok := p.loadCredential()
	if !ok || time.Until(cred.AccessExpiresAt) > credentialRefreshWindow {
		return nil
	}
	p.credMu.Lock()
	defer p.credMu.Unlock()
	// Another request may have refreshed while this one waited for the lock.
	cred, ok = p.loadCredential()
	if !ok || time.Until(cred.AccessExpiresAt) > credentialRefreshWindow {
		return nil
	}
	return p.refreshCredential(ctx, exec, cred)
}

func (p *Plugin) refreshCredential(ctx context.Context, exec storage.Executor, cred *oauthCredential) error {
	if !cred.RefreshExpiresAt.IsZero() && !cred.RefreshExpiresAt.After(time.Now()) {
		p.clearCredential()
		return errCredentialRejected
	}
	// Device-flow refresh tokens do not need the app's client secret.
	result, err := p.post(ctx, deviceTokenURL, url.Values{
		"client_id":     {deviceClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {cred.RefreshToken},
	})
	if err != nil {
		return err
	}
	token := stringValue(result["access_token"])
	if !tokenRE.MatchString(token) {
		switch stringValue(result["error"]) {
		case "bad_refresh_token", "invalid_grant":
			p.clearCredential()
			return errCredentialRejected
		default:
			return errors.New("GitHub sign-in could not be refreshed")
		}
	}
	if err := p.storeToken(ctx, exec, token); err != nil {
		return err
	}
	next, ok := credentialFromResponse(result)
	if !ok {
		// Keep the current refresh token if GitHub omits a replacement; only
		// the access lifetime changed.
		expiresAt, expires := credentialExpiry(result["expires_in"])
		if !expires {
			p.clearCredential()
			return errCredentialRejected
		}
		next = &oauthCredential{RefreshToken: cred.RefreshToken, AccessExpiresAt: expiresAt}
	}
	return p.saveCredential(next)
}

// Package github is the built-in storage and sign-in plugin for GitHub.
//
// It implements storage.Remote: Folio owns the checkout, the model run, the
// commit, and the audit trail; this package owns the remote, the credential,
// and the browser flow that creates it. The plugin is compiled into the
// binary and mounted by cmd/folio; it is not a service of its own.
package github

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"folio/internal/plugin"
	"folio/internal/storage"
)

//go:embed frontend
var frontendFS embed.FS

var (
	repositoryNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	tokenRE          = regexp.MustCompile(`^[A-Za-z0-9_.-]{20,512}$`)
	remoteRE         = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}\.git$`)
)

const (
	tokenEnv       = "GIT_AUTH_TOKEN"
	credentialHost = "github.com"
	// deviceClientID must belong to an OAuth App with Device Flow enabled, not
	// a GitHub App: GitHub App device tokens ignore the requested scopes, so
	// private repositories would never be listed or cloned.
	deviceClientID    = "Ov23lilrzgqsR1xcsbJk"
	maxAuthBodyBytes  = 128 * 1024
	credentialTimeout = 20 * time.Second
)

// The endpoints are variables so tests can point them at a local server. They
// are never configured at runtime.
var (
	apiURL         = "https://api.github.com"
	deviceURL      = "https://github.com/login/device/code"
	deviceTokenURL = "https://github.com/login/oauth/access_token"
)

// Plugin holds the private state of the GitHub integration.
type Plugin struct {
	dataDir    string
	secretsDir string
	authPath   string
	credPath   string

	mu      sync.Mutex
	pending *deviceFlow

	// statusMu guards the short-lived auth-mode cache. It is separate from mu
	// because the device flow holds mu while it asks for the auth mode.
	statusMu  sync.Mutex
	statusAt  time.Time
	statusVal string
}

// New prepares the plugin against one private data directory. It validates
// GIT_AUTH_TOKEN and writes the git credential file once, when that variable
// is the source of truth.
func New(dataDir string) (*Plugin, error) {
	dataDir = filepath.Clean(dataDir)
	plugin := &Plugin{
		dataDir:    dataDir,
		secretsDir: filepath.Join(dataDir, "secrets"),
		authPath:   filepath.Join(dataDir, "secrets", "github-auth.json"),
		credPath:   filepath.Join(dataDir, "secrets", "git-credentials"),
	}
	if err := os.MkdirAll(plugin.secretsDir, 0o700); err != nil {
		return nil, errors.New("github secret directory is unavailable")
	}
	if err := plugin.prepareToken(); err != nil {
		return nil, err
	}
	return plugin, nil
}

func (p *Plugin) ID() string    { return "github" }
func (p *Plugin) Title() string { return "GitHub" }

func (p *Plugin) Assets() fs.FS {
	sub, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		return nil
	}
	return sub
}

func (p *Plugin) Storage() storage.Remote { return p }

func (p *Plugin) Routes() []plugin.Route {
	return []plugin.Route{
		{Method: "POST", Path: "/auth/start", Handler: p.startAuth},
		{Method: "POST", Path: "/auth/poll", Handler: p.pollAuth},
		{Method: "POST", Path: "/auth/logout", Handler: p.logout},
		{Method: "GET", Path: "/status", Handler: p.status},
		{Method: "GET", Path: "/repositories", Handler: p.repositories},
	}
}

// prepareToken writes the deployment token into a private git credential file.
// The token never enters a command line or a model environment.
func (p *Plugin) prepareToken() error {
	token := strings.TrimSpace(os.Getenv(tokenEnv))
	if token == "" {
		return nil
	}
	if !tokenRE.MatchString(token) {
		return errors.New("GIT_AUTH_TOKEN is invalid")
	}
	entry := "https://x-access-token:" + token + "@" + credentialHost + "\n"
	if err := os.WriteFile(p.credPath, []byte(entry), 0o600); err != nil {
		return errors.New("git credential file could not be written")
	}
	return os.Chmod(p.credPath, 0o600)
}

func (p *Plugin) tokenFromEnv() string {
	token := strings.TrimSpace(os.Getenv(tokenEnv))
	if tokenRE.MatchString(token) {
		return token
	}
	return ""
}

// credentialArgs is the git configuration that lets the remote ops authenticate.
func (p *Plugin) credentialArgs(exec storage.Executor) []string {
	if p.tokenFromEnv() != "" {
		return []string{"credential.helper=store --file=" + p.credPath}
	}
	if p.ghSignedIn(context.Background(), exec) {
		return []string{"credential.helper=!gh auth git-credential"}
	}
	return nil
}

func (p *Plugin) ghSignedIn(ctx context.Context, exec storage.Executor) bool {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return false
	}
	out, err := exec.Exec(ctx, []string{gh, "auth", "status", "--hostname", credentialHost}, p.dataDir, "", 15*time.Second)
	return err == nil && out.Code == 0
}

func (p *Plugin) ghToken(ctx context.Context, exec storage.Executor) (string, error) {
	if token := p.tokenFromEnv(); token != "" {
		return token, nil
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return "", errors.New("GitHub sign-in is unavailable")
	}
	out, err := exec.Exec(ctx, []string{gh, "auth", "token", "--hostname", credentialHost}, p.dataDir, "", credentialTimeout)
	if err != nil || out.Code != 0 || out.Truncated {
		return "", errors.New("GitHub sign-in is unavailable")
	}
	token := strings.TrimSpace(out.Stdout)
	if !tokenRE.MatchString(token) {
		return "", errors.New("GitHub sign-in is unavailable")
	}
	return token, nil
}

func (p *Plugin) Status(ctx context.Context, exec storage.Executor) (map[string]any, error) {
	mode := p.authMode(ctx, exec)
	return map[string]any{"plugin": p.ID(), "auth": mode}, nil
}

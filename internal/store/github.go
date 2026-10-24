package store

import (
	"os"
	"strings"
	"time"
)

// Folio is not a code-host client. It needs exactly one thing from GitHub:
// an HTTPS git credential for the context repository. That credential is
// supplied by the deployment (GIT_AUTH_TOKEN) or inherited from an existing gh
// session. Everything that treats GitHub as a *service* — login, repository
// discovery, issues, pull requests, checks — belongs to an integration plugin
// that calls the hook API. See docs/integration-plugins.md.

const (
	// gitTokenEnv names the deployment secret that authenticates the context
	// repository remote. It is a deployment secret, never a UI-managed login.
	gitTokenEnv = "GIT_AUTH_TOKEN"
	// gitCredentialHost is the host git-credential-store writes an entry for.
	// Folio's repository is a GitHub HTTPS remote today; a different host only
	// changes this line and the remote URL.
	gitCredentialHost = "github.com"
)

// prepareGitCredential turns the deployment secret into a private credential
// file that git can use without exposing the token in a command line or in the
// environment of model runs.
func (s *Store) prepareGitCredential() error {
	raw := strings.TrimSpace(os.Getenv(gitTokenEnv))
	if raw == "" {
		return nil
	}
	if !githubTokenRE.MatchString(raw) {
		return errStore("GIT_AUTH_TOKEN is invalid")
	}
	if strings.ContainsAny(s.gitCredentialPath, " \t\"'") {
		return errStore("GIT_AUTH_TOKEN needs a data path without spaces")
	}
	line := "https://x-access-token:" + raw + "@" + gitCredentialHost + "\n"
	if err := os.WriteFile(s.gitCredentialPath, []byte(line), 0o600); err != nil {
		return errStore("git credential could not be stored")
	}
	s.gitCredentialSource = "token"
	return nil
}

// gitCredentialMode explains how git authenticates, without returning the
// credential itself.
func (s *Store) gitCredentialMode() string {
	if s.gitCredentialSource == "token" {
		return "token"
	}
	if s.ghAuthenticated() {
		return "gh"
	}
	return "missing"
}

// gitCredentialArgs returns the git config overrides that select the credential
// helper for this process. The gh fallback keeps deployments that already have a
// gh session working without a migration.
func (s *Store) gitCredentialArgs() []string {
	if s.gitCredentialSource == "token" {
		return []string{
			"-c", "credential.helper=",
			"-c", "credential.helper=store --file=" + s.gitCredentialPath,
		}
	}
	return []string{
		"-c", "credential.helper=",
		"-c", "credential.helper=!gh auth git-credential",
	}
}

// ghAuthenticated reports whether an existing gh session can still provide a
// credential. It is the compatibility path for deployments that logged in
// before the deployment secret existed.
func (s *Store) ghAuthenticated() bool {
	if s.gitCredentialSource == "token" {
		return false
	}
	gh, err := s.executable("gh")
	if err != nil {
		return false
	}
	result, runErr := s.runCommand([]string{gh, "auth", "token", "--hostname", gitCredentialHost}, s.dataDir, "", 20*time.Second, false, nil)
	if runErr != nil || result.code != 0 || result.truncated {
		return false
	}
	return githubTokenRE.MatchString(strings.TrimSpace(result.stdout))
}

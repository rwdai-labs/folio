package store

import (
	"context"
	"time"

	"folio/internal/storage"
)

// executor is the process boundary offered to storage plugins. It keeps the
// hardened git configuration and the scrubbed environment in Folio: a plugin
// chooses what to run and which credential to use, never how to run it.
type executor struct {
	store *Store
}

func (e executor) Git(ctx context.Context, dir string, credential []string, timeout time.Duration, args []string) (storage.Output, error) {
	if timeout <= 0 || timeout > maxGitTimeout {
		timeout = maxGitTimeout
	}
	result, err := e.store.runCommandContext(ctx, mustGitCommand(e.store, args, credential), dir, "", timeout, false, nil)
	return storage.Output{Stdout: result.stdout, Stderr: result.stderr, Code: result.code, Truncated: result.truncated}, err
}

func (e executor) Exec(ctx context.Context, argv []string, dir string, stdin string, timeout time.Duration) (storage.Output, error) {
	if timeout <= 0 || timeout > maxGitTimeout {
		timeout = maxGitTimeout
	}
	result, err := e.store.runCommandContext(ctx, argv, dir, stdin, timeout, false, nil)
	return storage.Output{Stdout: result.stdout, Stderr: result.stderr, Code: result.code, Truncated: result.truncated}, err
}

func (e executor) LookPath(name string) (string, error) {
	return e.store.executable(name)
}

// mustGitCommand keeps the git binary and the hardened configuration in one
// place. The credential entries supplied by the plugin sit after the reset, so
// a plugin cannot re-enable an inherited helper.
func mustGitCommand(s *Store, args []string, credential []string) []string {
	command, err := s.gitCommand(args, credential)
	if err != nil {
		// git is required at startup; an empty command is reported by runCommand.
		return nil
	}
	return command
}

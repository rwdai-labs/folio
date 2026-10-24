// Package storage defines the boundary between Folio and the code host that
// holds the context repository.
//
// Folio owns the checkout, the model run, the commit, and the audit trail. A
// storage plugin owns the remote: how to authenticate, where the bytes live,
// and what "pull" and "push" mean. GitHub is the first implementation; a
// network drive or an S3 bucket can implement the same interface without
// touching the maintenance worker.
package storage

import (
	"context"
	"time"
)

// Resolved is the canonical identity of a connected repository.
type Resolved struct {
	Name   string // "owner/name" as shown to the owner
	Remote string // canonical remote URL used by the checkout
}

// SyncResult describes what a pull did.
type SyncResult struct {
	Status     string // "cloned", "up_to_date", "synced"
	Branch     string
	Head       string
	RemoteHead string
}

// Options carries the paths and identity of one operation. Dir is an absolute
// private path; Remote is empty while the repository is being connected.
type Options struct {
	Dir    string
	Remote string
	Branch string
	// TestMode allows local file remotes. Production never sets it.
	TestMode bool
}

// Output is one bounded process result.
type Output struct {
	Stdout    string
	Stderr    string
	Code      int
	Truncated bool
}

// Executor is the safe process boundary owned by Folio. A plugin never calls
// exec directly: it asks this interface, and Folio keeps the hardened git
// configuration and the scrubbed environment. Credential entries are supplied
// by the plugin because the credential belongs to the plugin.
type Executor interface {
	Git(ctx context.Context, dir string, credential []string, timeout time.Duration, args []string) (Output, error)
	Exec(ctx context.Context, argv []string, dir string, stdin string, timeout time.Duration) (Output, error)
	LookPath(name string) (string, error)
}

// Remote is one storage backend.
type Remote interface {
	ID() string
	// Resolve validates owner input and returns the canonical repository.
	Resolve(input string, testMode bool) (Resolved, error)
	// Validate checks that a configured remote belongs to this backend.
	Validate(remote string, testMode bool) error
	// Sync clones a missing checkout or fast-forwards an existing one.
	Sync(ctx context.Context, exec Executor, opts Options) (SyncResult, error)
	// Head returns the remote branch head without touching the checkout.
	Head(ctx context.Context, exec Executor, opts Options) (string, error)
	// Publish pushes the committed HEAD and verifies the remote branch.
	Publish(ctx context.Context, exec Executor, opts Options) (string, error)
	// Status reports authentication state for the UI. It never returns secrets.
	Status(ctx context.Context, exec Executor) (map[string]any, error)
}

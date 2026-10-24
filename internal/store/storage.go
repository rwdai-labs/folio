package store

import (
	"context"

	"folio/internal/storage"
)

// ResolveLayout exposes the resolved private data directory so the entrypoint
// can build built-in plugins against exactly the path the store uses.
func ResolveLayout(raw string) (string, error) {
	return resolvePath(raw)
}

// Executor is the process boundary the Web layer hands to built-in plugins.
// It is the same executor the storage plugin gets, so there is exactly one
// place where a process is started and one environment policy.
func (s *Store) Executor() storage.Executor { return executor{s} }

// storageStatus reports how the storage plugin authenticates, never a secret.
func (s *Store) storageStatus() map[string]any {
	if s.remote == nil {
		return map[string]any{"plugin": "", "auth": "missing"}
	}
	status, err := s.remote.Status(context.Background(), executor{s})
	if err != nil || status == nil {
		return map[string]any{"plugin": s.remote.ID(), "auth": "missing"}
	}
	return status
}

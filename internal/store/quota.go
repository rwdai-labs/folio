package store

import (
	"context"

	"folio/internal/quota"
)

// Quotas returns the bounded subscription-allowance snapshot. The quota source
// is optional: an absent data/quotas.json yields Configured=false, not an
// error. Source failures are carried in Snapshot.Error so the dashboard can
// keep showing the last successful reading.
func (s *Store) Quotas(refresh bool) (quota.Snapshot, error) {
	return s.quotaService.Snapshot(context.Background(), refresh), nil
}

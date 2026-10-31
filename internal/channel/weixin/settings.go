package weixin

import (
	"errors"
	"os"
	"path/filepath"
)

// settingsFile stores the owner decision that follows a successful QR login.
// The scanner identity returned by the transport is only a proposal: PendingOwner
// holds it until an authenticated administrator confirms the same ID, and OwnerID
// stays empty until then. Credentials alone never authorize a poller to start.
const settingsFile = "settings.json"

// ownerMaxBytes bounds an opaque scanner identity copied from the transport.
const ownerMaxBytes = 512

// Settings is the private, operator-confirmed channel configuration. It is
// stored next to the credentials inside the exclusive-locked directory so a
// running poller and an interactive login cannot disagree about the owner.
type Settings struct {
	Version      int    `json:"version"`
	OwnerID      string `json:"owner_id,omitempty"`
	PendingOwner string `json:"pending_owner_id,omitempty"`
	Enabled      bool   `json:"enabled,omitempty"`
}

func validOwner(value string) bool {
	return value == "" || validOpaque(value, ownerMaxBytes)
}

// Settings reads the operator decision, treating an absent file as "not yet
// configured" rather than as an error. Corrupt or over-permissive files fail
// closed instead of silently dropping an authorization decision.
func (d *Disk) Settings() (Settings, error) {
	var settings Settings
	err := d.read(settingsFile, &settings)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{Version: 1}, nil
	}
	if err != nil {
		return Settings{}, ErrState
	}
	if settings.Version != 1 || !validOwner(settings.OwnerID) || !validOwner(settings.PendingOwner) {
		return Settings{}, ErrState
	}
	return settings, nil
}

// SaveSettings persists one complete decision. A pending owner without an
// enabled channel is the normal post-login state; an enabled channel without
// an owner would be unusable, so it is rejected here.
func (d *Disk) SaveSettings(settings Settings) error {
	if !validOwner(settings.OwnerID) || !validOwner(settings.PendingOwner) {
		return ErrState
	}
	if settings.Enabled && settings.OwnerID == "" {
		return ErrState
	}
	settings.Version = 1
	return d.save(settingsFile, settings)
}

// HasCredentials reports whether a completed login exists, without parsing or
// exposing the file. Missing credentials are a normal unconfigured state.
func (d *Disk) HasCredentials() bool {
	path := filepath.Join(d.dir, "credentials.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	return info.Size() > 0 && info.Mode().Perm()&0o077 == 0
}

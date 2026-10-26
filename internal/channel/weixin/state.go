package weixin

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"folio/internal/channel"
)

const maxStateBytes = 8 * 1024 * 1024
const maxRecent = 4096

// Disk is exclusively held for an entire login or poller lifetime. Credentials
// and recovery state share a private directory, never the context checkout.
type Disk struct {
	dir  string
	lock *os.File
}

type delivery struct {
	Message      channel.Message `json:"message"`
	ContextToken string          `json:"context_token"`
	Reply        string          `json:"reply,omitempty"`
	Ready        bool            `json:"ready,omitempty"`
}

type syncState struct {
	Version   int        `json:"version"`
	AccountID string     `json:"account_id"`
	Cursor    string     `json:"cursor"`
	Pending   []delivery `json:"pending"`
	Recent    []string   `json:"recent"`
}

func OpenDisk(dir string) (*Disk, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, ErrState
	}
	// This includes both a checked-out public source tree and a private
	// context Git tree. Secrets and cursors must stay outside either one.
	for parent := absolute; ; parent = filepath.Dir(parent) {
		if _, err := os.Lstat(filepath.Join(parent, ".git")); err == nil {
			return nil, ErrState
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	// Refuse symlinks in every existing component, including the data root.
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(absolute, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if os.Mkdir(current, 0o700) != nil {
				return nil, ErrState
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrState
		}
	}
	if os.Chmod(absolute, 0o700) != nil {
		return nil, ErrState
	}
	d := &Disk{dir: absolute}
	path := filepath.Join(absolute, "channel.lock")
	if !regularOrMissing(path) {
		return nil, ErrState
	}
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, ErrState
	}
	if lock.Chmod(0o600) != nil {
		_ = lock.Close()
		return nil, ErrState
	}
	if err := acquireLock(lock); err != nil {
		_ = lock.Close()
		return nil, err
	}
	d.lock = lock
	return d, nil
}

func (d *Disk) Close() error { return d.lock.Close() }

func regularOrMissing(path string) bool {
	info, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0)
}

func (d *Disk) read(name string, out any) error {
	path := filepath.Join(d.dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxStateBytes {
		return ErrState
	}
	f, err := os.Open(path)
	if err != nil {
		return ErrState
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil || len(raw) > maxStateBytes || json.Unmarshal(raw, out) != nil {
		return ErrState
	}
	return nil
}

// save commits one complete snapshot. A crash leaves either the old snapshot
// or the new one, never a cursor detached from its accepted inbox.
func (d *Disk) save(name string, value any) error {
	path := filepath.Join(d.dir, name)
	if !regularOrMissing(path) {
		return ErrState
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxStateBytes {
		return ErrState
	}
	f, err := os.CreateTemp(d.dir, ".state-*")
	if err != nil {
		return ErrState
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return ErrState
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return ErrState
	}
	if err := f.Close(); err != nil {
		return ErrState
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return ErrState
	}
	dir, err := os.Open(d.dir)
	if err != nil {
		return ErrState
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return ErrState
	}
	return nil
}

func (d *Disk) Credentials() (Credentials, error) {
	var creds Credentials
	if err := d.read("credentials.json", &creds); err != nil {
		return creds, ErrState
	}
	if _, err := NewClient(creds); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

func (d *Disk) saveCredentials(creds Credentials) error {
	if _, err := NewClient(creds); err != nil {
		return err
	}
	// Never apply another bot's cursor or pending replies. An interrupted
	// account switch fails closed on the mismatch instead of resetting data.
	var old Credentials
	err := d.read("credentials.json", &old)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && old.AccountID != creds.AccountID {
		return errors.New("weixin: use a separate data directory for another account")
	}
	return d.save("credentials.json", creds)
}

func (d *Disk) state(account string) (*syncState, error) {
	var state syncState
	err := d.read("sync.json", &state)
	if errors.Is(err, os.ErrNotExist) {
		return &syncState{Version: 1, AccountID: account}, nil
	}
	if err != nil || state.Version != 1 || state.AccountID != account || len(state.Pending) > 100 || len(state.Recent) > maxRecent || len(state.Cursor) > 256*1024 {
		return nil, ErrState
	}
	for _, p := range state.Pending {
		if p.Message.Channel != "weixin" || p.Message.AccountID != account || !validOpaque(p.Message.ID, 512) || !validOpaque(p.Message.SenderID, 512) || p.Message.ConversationID != p.Message.SenderID || len(p.Message.Attachments) != 0 || len(p.Message.Text) > maxTextBytes || len(p.Reply) > maxTextBytes || !validOpaque(p.ContextToken, 8192) {
			return nil, ErrState
		}
	}
	return &state, nil
}

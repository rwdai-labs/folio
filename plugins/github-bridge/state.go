package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

const (
	maxStateBytes  = 512 * 1024
	maxSeenPerRepo = 500
)

type bridgeState struct {
	Version int                   `json:"version"`
	Repos   map[string]*repoState `json:"repos"`
}

// repoState is the per-repository cursor. ETag avoids refetching an unchanged
// page; Seen maps issue number to its last observed updated_at so an edit emits
// exactly one event; Baseline suppresses a flood on first sight.
type repoState struct {
	ETag      string            `json:"etag,omitempty"`
	Seen      map[string]string `json:"seen,omitempty"`
	Baseline  bool              `json:"baseline"`
	HoldUntil string            `json:"hold_until,omitempty"`
}

func loadState(path string) (*bridgeState, error) {
	state := &bridgeState{Version: 1, Repos: map[string]*repoState{}}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return nil, errors.New("state file is unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxStateBytes {
		return nil, errors.New("state file is invalid")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > maxStateBytes {
		return nil, errors.New("state file is unreadable")
	}
	if err := json.Unmarshal(raw, state); err != nil {
		return nil, errors.New("state file is invalid")
	}
	if state.Repos == nil {
		state.Repos = map[string]*repoState{}
	}
	return state, nil
}

// save writes atomically so a crash cannot leave a half-written cursor that
// would replay or skip events.
func (state *bridgeState) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("state directory is unavailable")
	}
	for _, repo := range state.Repos {
		if len(repo.Seen) > maxSeenPerRepo {
			repo.Seen = pruneSeen(repo.Seen)
		}
	}
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > maxStateBytes {
		return errors.New("state could not be encoded")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".bridge-state-*")
	if err != nil {
		return errors.New("state file is unwritable")
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		temp.Close()
		return errors.New("state file is unwritable")
	}
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return errors.New("state file is unwritable")
	}
	if err := temp.Close(); err != nil {
		return errors.New("state file is unwritable")
	}
	if err := os.Rename(name, path); err != nil {
		return errors.New("state file is unwritable")
	}
	return nil
}

// pruneSeen keeps the highest issue numbers: an issue that falls out of the
// window is simply re-observed as new if it changes again, which is safe.
func pruneSeen(seen map[string]string) map[string]string {
	numbers := make([]int, 0, len(seen))
	for key := range seen {
		if number, err := strconv.Atoi(key); err == nil {
			numbers = append(numbers, number)
		}
	}
	sort.Ints(numbers)
	keep := map[string]string{}
	for _, number := range numbers[len(numbers)-maxSeenPerRepo:] {
		key := strconv.Itoa(number)
		keep[key] = seen[key]
	}
	return keep
}

func holdActive(repo *repoState, now time.Time) bool {
	if repo == nil || repo.HoldUntil == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, repo.HoldUntil)
	if err != nil {
		return false
	}
	return now.Before(until)
}

package store

import (
	"encoding/json"
	"os"
	"path/filepath"

	contextrepo "folio/internal/context"
)

func (s *Store) maintenanceStatePath() string {
	return filepath.Join(s.runtimeDir, "maintenance-state.json")
}

type maintenanceFinding struct {
	contextrepo.MaintenanceIssue
	Hash string `json:"hash"`
}

func (s *Store) maintenanceFindingsPath() string {
	return filepath.Join(s.runtimeDir, "maintenance-review.json")
}

func (s *Store) maintenanceInput() (*contextrepo.Router, contextrepo.ReviewData, error) {
	router, err := contextrepo.New(s.repositoryDir)
	if err != nil {
		return nil, contextrepo.ReviewData{}, err
	}
	previous := map[string]string{}
	if _, err := os.Lstat(s.maintenanceStatePath()); err == nil {
		value, err := s.loadJSON(s.maintenanceStatePath(), 512*1024)
		if err != nil {
			return nil, contextrepo.ReviewData{}, err
		}
		raw, _ := json.Marshal(value)
		if json.Unmarshal(raw, &previous) != nil || len(previous) > 4096 {
			return nil, contextrepo.ReviewData{}, errStore("maintenance checkpoint is invalid")
		}
	} else if !os.IsNotExist(err) {
		return nil, contextrepo.ReviewData{}, err
	}
	data, err := router.MaintenanceInput(previous)
	if err != nil {
		return router, data, err
	}
	if _, statErr := os.Lstat(s.maintenanceFindingsPath()); statErr == nil {
		value, loadErr := s.loadJSON(s.maintenanceFindingsPath(), 256*1024)
		if loadErr != nil {
			return router, data, loadErr
		}
		raw, _ := json.Marshal(value)
		var findings []maintenanceFinding
		if json.Unmarshal(raw, &findings) != nil || len(findings) > 60 {
			return router, data, errStore("maintenance findings are invalid")
		}
		for _, finding := range findings {
			if finding.Hash == "" || previous[finding.Source] != finding.Hash {
				continue
			}
			if _, err := s.validateTarget(finding.Source, false); err != nil {
				continue
			}
			content, _, readErr := readRegularFile(filepath.Join(s.repositoryDir, filepath.FromSlash(finding.Source)), maxAutoContentBytes)
			if readErr != nil || sha256Hex(content) != finding.Hash {
				continue
			}
			duplicate := false
			for _, issue := range data.Issues {
				if issue == finding.MaintenanceIssue {
					duplicate = true
					break
				}
			}
			if !duplicate && len(data.Issues) < 60 {
				data.Issues = append(data.Issues, finding.MaintenanceIssue)
			}
		}
	} else if !os.IsNotExist(statErr) {
		return router, data, statErr
	}
	return router, data, err
}

func (s *Store) saveMaintenanceFindings(issues []contextrepo.MaintenanceIssue) error {
	findings := []maintenanceFinding{}
	for _, issue := range issues {
		if _, err := s.validateTarget(issue.Source, false); err != nil {
			continue
		}
		raw, _, err := readRegularFile(filepath.Join(s.repositoryDir, filepath.FromSlash(issue.Source)), maxAutoContentBytes)
		if err != nil {
			continue
		}
		findings = append(findings, maintenanceFinding{issue, sha256Hex(raw)})
	}
	return s.savePrivateJSON(s.maintenanceFindingsPath(), findings)
}

// Store only fingerprints, never context bodies. A failed apply/push does not
// call this method. Use the resulting bytes so our own commit is acknowledged.
func (s *Store) saveMaintenanceCheckpoint(files map[string]string) error {
	state := map[string]string{}
	if _, err := os.Lstat(s.maintenanceStatePath()); err == nil {
		value, err := s.loadJSON(s.maintenanceStatePath(), 512*1024)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(value)
		if json.Unmarshal(raw, &state) != nil {
			return errStore("maintenance checkpoint is invalid")
		}
	}
	for path := range files {
		validated, err := s.validateTarget(path, false)
		if err != nil {
			return err
		}
		raw, _, err := readRegularFile(filepath.Join(s.repositoryDir, filepath.FromSlash(validated)), maxAutoContentBytes)
		if err != nil {
			return err
		}
		if sha256Hex(raw) != files[path] {
			return errStore("maintenance content changed before checkpoint")
		}
		state[path] = files[path]
	}
	for path := range state {
		if _, err := s.validateTarget(path, false); err != nil {
			delete(state, path)
		}
	}
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > 512*1024 || len(state) > 4096 {
		return errStore("maintenance checkpoint is too large")
	}
	return writeAtomic(s.maintenanceStatePath(), append(raw, '\n'), 0o600)
}

func (s *Store) queueMaintenanceContinuation(run map[string]any) {
	if run == nil || (run["status"] != "pushed" && run["status"] != "no_changes") {
		return
	}
	remaining, _ := run["remaining"].(int)
	if remaining <= 0 {
		return
	}
	kinds, err := s.loadTaskKinds()
	if err != nil {
		run["continuation_queued"] = false
		return
	}
	for _, kind := range kinds.Kinds {
		if kind.Action != kindActionMaintenance {
			continue
		}
		_, err := s.CreateTask(kind.ID, "maintenance:continuation", "Continue context organization", kindDefaultPriority(kind), nil)
		run["continuation_queued"] = err == nil
		return
	}
	run["continuation_queued"] = false
}

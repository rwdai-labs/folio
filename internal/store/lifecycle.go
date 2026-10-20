package store

import (
	"os"
	"path/filepath"
	"time"
)

// ConnectRepository binds the external checkout to one narrow GitHub name.
func (s *Store) ConnectRepository(fullName string) (map[string]any, error) {
	owner, name, err := s.validateFullName(fullName)
	if err != nil {
		return nil, err
	}
	expectedRemote := "https://github.com/" + owner + "/" + name + ".git"
	return s.withLock(func() (map[string]any, error) {
		target := s.repositoryDir
		if info, statErr := os.Lstat(target); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, errStore("repository path must not be a symlink")
			}
			gitInfo, gitErr := os.Lstat(filepath.Join(target, ".git"))
			if gitErr != nil || gitInfo.Mode()&os.ModeSymlink != 0 || (!gitInfo.IsDir() && !gitInfo.Mode().IsRegular()) {
				return nil, errStore("repository directory is not a Git checkout")
			}
			if _, verifyErr := s.verifyRemote(expectedRemote); verifyErr != nil {
				return nil, verifyErr
			}
		} else if os.IsNotExist(statErr) {
			if _, cloneErr := s.runGit([]string{"clone", "--no-tags", expectedRemote, target}, s.dataDir, 5*time.Minute, nil); cloneErr != nil {
				return nil, errStore("repository could not be connected")
			}
			if _, verifyErr := s.verifyRemote(expectedRemote); verifyErr != nil {
				return nil, verifyErr
			}
		} else {
			return nil, errStore("repository path is unavailable")
		}
		branch, branchErr := s.branch()
		if branchErr != nil {
			return nil, branchErr
		}
		head, headErr := s.gitHead()
		if headErr != nil {
			return nil, headErr
		}
		saved := s.loadState()
		previous := mapValue(saved, "repository")
		if previousName, ok := previous["full_name"].(string); ok && previousName != "" && previousName != owner+"/"+name {
			return nil, errStore("a different repository is already connected")
		}
		saved["repository"] = map[string]any{
			"full_name": owner + "/" + name,
			"remote":    expectedRemote,
			"branch":    branch,
			"head":      head,
		}
		if err := s.saveState(saved); err != nil {
			return nil, err
		}
		return map[string]any{
			"status":    "connected",
			"full_name": owner + "/" + name,
			"branch":    branch,
			"head":      head,
		}, nil
	})
}

// SyncRepository fast-forwards a clean checkout from its fixed origin.
func (s *Store) SyncRepository() (map[string]any, error) {
	return s.withLock(func() (map[string]any, error) {
		return s.syncRepositoryLocked()
	})
}

func (s *Store) syncRepositoryLocked() (map[string]any, error) {
	if err := s.requireRepository(); err != nil {
		return nil, err
	}
	if _, err := s.verifyRemote(s.configuredRemote()); err != nil {
		return nil, err
	}
	if err := s.requireCleanCheckout(); err != nil {
		return nil, err
	}
	branch, err := s.branch()
	if err != nil {
		return nil, err
	}
	remoteHead, err := s.remoteHead(branch)
	if err != nil {
		return nil, err
	}
	localHead, err := s.gitHead()
	if err != nil {
		return nil, err
	}
	if remoteHead == localHead {
		if err := s.recordRepositoryHead(localHead, branch); err != nil {
			return nil, err
		}
		return map[string]any{"status": "up_to_date", "branch": branch, "head": localHead}, nil
	}
	if _, err := s.runGit([]string{"fetch", "--no-tags", "origin", "refs/heads/" + branch + ":refs/remotes/origin/" + branch}, s.repositoryDir, 5*time.Minute, nil); err != nil {
		return nil, errStore("repository sync failed")
	}
	if _, err := s.runGit([]string{"merge", "--ff-only", "refs/remotes/origin/" + branch}, s.repositoryDir, 2*time.Minute, nil); err != nil {
		return nil, errStore("repository sync failed")
	}
	head, err := s.gitHead()
	if err != nil {
		return nil, err
	}
	if err := s.recordRepositoryHead(head, branch); err != nil {
		return nil, err
	}
	return map[string]any{"status": "synced", "branch": branch, "head": head, "remote": remoteHead}, nil
}

// RunMaintenance invokes only the installed folio binary. The checkout is
// supplied as data through explicit environment variables.
func (s *Store) RunMaintenance(mode, model, runner string) (map[string]any, error) {
	if mode != "report" && mode != "agent" {
		return nil, errStore("maintenance mode must be report or agent")
	}
	runner, err := normalizeRunner(runner)
	if err != nil {
		return nil, err
	}
	if model != "" && !modelRE.MatchString(model) {
		return nil, errStore("model name is invalid")
	}
	return s.withLock(func() (map[string]any, error) {
		return s.runMaintenanceLocked(mode, model, runner)
	})
}

func normalizeRunner(runner string) (string, error) {
	if runner == "" {
		return "codex", nil
	}
	if runner != "codex" && runner != "dsh" {
		return "", errStore("runner must be codex or dsh")
	}
	return runner, nil
}

// SchedulerTick runs one serialized automatic maintenance pass: sync the
// checkout, ask the review runner for bounded edits, validate them, commit,
// and push. The full result is persisted as an automation run.
func (s *Store) SchedulerTick() (map[string]any, error) {
	return s.RunAutomation("scheduler", "", "codex")
}

func (s *Store) runMaintenanceLocked(mode, model, runner string) (map[string]any, error) {
	if err := s.requireRepository(); err != nil {
		return nil, err
	}
	command, err := s.installedFolioCommand()
	if err != nil {
		return nil, err
	}
	available := s.runnerAvailable(runner)
	if mode == "agent" && !available {
		return map[string]any{
			"status":          "skipped",
			"mode":            mode,
			"runner":          runner,
			"model_available": false,
			"reason":          "model_unavailable",
		}, nil
	}
	if mode == "report" {
		command = append(command, "maintain", "--device", "Docker", "--report-dir", s.reportsDir)
	} else {
		command = append(command, "agent", "--device", "Docker", "--runtime-dir", s.runtimeDir, "--runner", runner)
		if model != "" {
			command = append(command, "--model", model)
		}
	}
	env := map[string]string{
		"FOLIO_CONTEXT_ROOT": s.repositoryDir,
		"FOLIO_RUNTIME":      s.runtimeDir,
		"CODEX_HOME":         s.codexDir,
		"DSH_HOME":           s.dshDir,
	}
	for key, value := range dshProviderEnv() {
		env[key] = value
	}
	result, err := s.runCommand(command, s.toolRoot, "", 15*time.Minute, true, env)
	if err != nil {
		return nil, errStore("maintenance operation failed")
	}
	response := map[string]any{
		"status":          "completed",
		"mode":            mode,
		"runner":          runner,
		"model_available": available,
		"finished_at":     nowString(),
	}
	if mode == "report" {
		reportPath := filepath.Join(s.reportsDir, "maintenance-report.md")
		if info, statErr := os.Lstat(reportPath); statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() <= maxReportBytes {
			if data, readErr := os.ReadFile(reportPath); readErr == nil && utf8Valid(data) {
				response["report"] = publicText(string(data), maxReportBytes)
			}
		} else if result.stdout != "" {
			response["report"] = publicText(result.stdout, maxReportBytes)
		}
		response["report_path"] = reportPath
	} else if result.stdout != "" {
		response["report"] = publicText(result.stdout, maxTextRunes)
	}
	saved := s.loadState()
	saved["maintenance"] = map[string]any{
		"status":          "completed",
		"mode":            mode,
		"runner":          runner,
		"model_available": available,
		"finished_at":     response["finished_at"],
	}
	if err := s.saveState(saved); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Store) installedFolioCommand() ([]string, error) {
	launcher := filepath.Join(s.toolRoot, "folio")
	info, err := os.Lstat(launcher)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errStore("installed folio tool is unavailable")
	}
	return []string{launcher}, nil
}

func resultString(value map[string]any, key, fallback string) string {
	if text, ok := value[key].(string); ok && text != "" {
		return text
	}
	return fallback
}

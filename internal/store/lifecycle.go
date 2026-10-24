package store

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"folio/internal/storage"
)

// ConnectRepository asks the storage plugin to resolve the requested
// repository and to create the checkout when it does not exist yet.
func (s *Store) ConnectRepository(fullName string) (map[string]any, error) {
	if s.remote == nil {
		return nil, errStore("no storage plugin is configured")
	}
	resolved, err := s.remote.Resolve(fullName, s.allowFileRemoteForTests)
	if err != nil {
		return nil, errStore("repository name is invalid")
	}
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
			if _, verifyErr := s.verifyRemote(resolved.Remote); verifyErr != nil {
				return nil, verifyErr
			}
		} else if os.IsNotExist(statErr) {
			if _, cloneErr := s.remote.Sync(context.Background(), executor{s}, storage.Options{
				Dir: target, Remote: resolved.Remote, TestMode: s.allowFileRemoteForTests,
			}); cloneErr != nil {
				return nil, errStore("repository could not be connected")
			}
			if _, verifyErr := s.verifyRemote(resolved.Remote); verifyErr != nil {
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
		if previousName, ok := previous["full_name"].(string); ok && previousName != "" && previousName != resolved.Name {
			return nil, errStore("a different repository is already connected")
		}
		saved["repository"] = map[string]any{
			"full_name": resolved.Name,
			"remote":    resolved.Remote,
			"branch":    branch,
			"head":      head,
		}
		if err := s.saveState(saved); err != nil {
			return nil, err
		}
		return map[string]any{
			"status":    "connected",
			"full_name": resolved.Name,
			"branch":    branch,
			"head":      head,
		}, nil
	})
}

// SyncRepository fast-forwards a clean checkout through the storage plugin.
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
	result, err := s.remote.Sync(context.Background(), executor{s}, storage.Options{
		Dir:      s.repositoryDir,
		Remote:   s.configuredRemote(),
		Branch:   branch,
		TestMode: s.allowFileRemoteForTests,
	})
	if err != nil {
		return nil, errStore("repository sync failed")
	}
	if err := s.recordRepositoryHead(result.Head, result.Branch); err != nil {
		return nil, err
	}
	response := map[string]any{"status": result.Status, "branch": result.Branch, "head": result.Head}
	if result.RemoteHead != "" && result.RemoteHead != result.Head {
		response["remote"] = result.RemoteHead
	}
	return response, nil
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
			if runner == "dsh" {
				if provider := dshProviderForModel(model); provider != "" {
					command = append(command, "--provider", provider)
				}
			}
		}
	}
	env := map[string]string{
		"FOLIO_CONTEXT_ROOT": s.repositoryDir,
		"FOLIO_RUNTIME":      s.runtimeDir,
		"CODEX_HOME":         s.codexDir,
		"DSH_HOME":           s.dshDir,
	}
	for key, value := range dshRunnerEnv() {
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

package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxAutoEdits        = 8
	maxAutoContentBytes = 64 * 1024
	maxAutoTotalBytes   = 512 * 1024
	maxAutoDiffBytes    = 128 * 1024
	maxAutoTotalDiff    = 256 * 1024
	maxAutoRunBytes     = 512 * 1024
	maxAutoSummaryRunes = 4096
	maxAutoRuns         = 40
)

var (
	autoRunIDRE      = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{8}$`)
	secretMaterialRE = regexp.MustCompile(`(?i)(-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|(?:DEEPSEEK|OPENAI|ANTHROPIC|GITHUB|GH)_?(?:API_)?KEY\s*[:=]\s*\S+)`)
)

// autoReport is the only model output accepted by automatic maintenance. The
// worker validates the shape first; the store validates every path and byte
// again before touching the canonical checkout.
type autoReport struct {
	Version     int        `json:"version"`
	Status      string     `json:"status"`
	Summary     string     `json:"summary"`
	Device      string     `json:"device"`
	Created     string     `json:"created"`
	Skipped     int        `json:"skipped"`
	InputDigest string     `json:"input_digest"`
	Edits       []autoEdit `json:"edits"`
}

type autoEdit struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	ClaimType string `json:"claim_type"`
	Reason    string `json:"reason"`
	Content   string `json:"content"`
}

type plannedAutoEdit struct {
	target    string
	source    string
	claimType string
	reason    string
	content   []byte
	old       []byte
	exists    bool
	action    string
	diff      string
}

// RunAutomation performs one serialized pull -> organize -> validate ->
// commit/push maintenance run. Every outcome, including skips and failures,
// is recorded in the private runtime for later inspection.
func (s *Store) RunAutomation(trigger, runner, model string) (map[string]any, error) {
	if trigger != "manual" && trigger != "scheduler" {
		return nil, errStore("automation trigger must be manual or scheduler")
	}
	normalized, err := normalizeRunner(runner)
	if err != nil {
		return nil, err
	}
	if model != "" && !modelRE.MatchString(model) {
		return nil, errStore("model name is invalid")
	}
	return s.withLock(func() (map[string]any, error) {
		return s.runAutomationLocked(trigger, normalized, model)
	})
}

func (s *Store) runAutomationLocked(trigger, runner, model string) (map[string]any, error) {
	started := time.Now()
	runID, err := newAutoRunID()
	if err != nil {
		return nil, errStore("automation run could not start")
	}
	run := map[string]any{
		"version":     1,
		"run_id":      runID,
		"trigger":     trigger,
		"status":      "running",
		"stage":       "repository",
		"started_at":  started.UTC().Format(time.RFC3339Nano),
		"runner":      runner,
		"summary":     "",
		"duration_ms": 0,
	}
	if model != "" {
		run["model"] = model
	}
	run["model_available"] = s.runnerAvailable(runner)
	if err := s.saveAutomationRun(run); err != nil {
		return nil, err
	}

	finish := func(status, stage, reason string, extra map[string]any) (map[string]any, error) {
		run["status"] = status
		run["stage"] = stage
		run["finished_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		run["duration_ms"] = time.Since(started).Milliseconds()
		if reason != "" {
			run["reason"] = reason
			if status == "failed" {
				run["error"] = reason
			}
		}
		for key, value := range extra {
			run[key] = value
		}
		if err := s.saveAutomationRun(run); err != nil {
			return nil, err
		}
		if err := s.recordSchedulerRun(run); err != nil {
			return nil, err
		}
		return run, nil
	}

	if err := s.requireRepository(); err != nil {
		return finish("skipped", "repository", "repository_not_connected", nil)
	}

	run["stage"] = "pull"
	pull, err := s.syncRepositoryLocked()
	if err != nil {
		run["pull"] = map[string]any{"status": "failed"}
		return finish("failed", "pull", "repository_sync_failed", nil)
	}
	run["pull"] = automationPullSummary(pull)
	run["stage"] = "organize"
	if err := s.saveAutomationRun(run); err != nil {
		return nil, err
	}

	if !s.runnerAvailable(runner) {
		run["summary"] = "Repository synced; no review runner is available."
		return finish("skipped", "organize", "model_unavailable", nil)
	}

	run["stage"] = "organize"
	report, err := s.runAutoAgentLocked(runner, model)
	if err != nil {
		return finish("failed", "organize", "maintenance_operation_failed", nil)
	}
	if secretMaterialRE.MatchString(report.Summary) {
		return finish("failed", "organize", "content_rejected", nil)
	}
	run["summary"] = publicText(report.Summary, maxAutoSummaryRunes)
	run["skipped"] = report.Skipped
	run["input_digest"] = publicText(report.InputDigest, 64)
	run["stage"] = "validate"
	if err := s.saveAutomationRun(run); err != nil {
		return nil, err
	}
	if len(report.Edits) == 0 {
		if strings.TrimSpace(report.Summary) == "" {
			run["summary"] = "No context changes were needed."
		}
		return finish("no_changes", "organize", "", nil)
	}

	run["stage"] = "validate"
	baseHead, err := s.gitHead()
	if err != nil {
		return finish("failed", "validate", "checkout_head_unavailable", nil)
	}
	run["base_head"] = baseHead
	applied, edits, stage, err := s.applyAutoEditsLocked(report.Edits, runID)
	if edits != nil {
		run["edits"] = edits
	}
	if err != nil {
		return finish("failed", stage, automationErrorCode(err), nil)
	}
	if len(edits) == 0 {
		run["edits"] = []any{}
		if strings.TrimSpace(report.Summary) == "" {
			run["summary"] = "No context changes were needed."
		}
		return finish("no_changes", "validate", "", nil)
	}
	run["edits"] = edits
	run["commit"] = applied.commit
	run["branch"] = applied.branch
	run["push"] = map[string]any{"status": "pushed", "remote_head": applied.commit}
	return finish("pushed", "done", "", nil)
}

type autoApplyResult struct {
	commit string
	branch string
}

func (s *Store) applyAutoEditsLocked(edits []autoEdit, runID string) (autoApplyResult, []any, string, error) {
	if len(edits) == 0 || len(edits) > maxAutoEdits {
		return autoApplyResult{}, nil, "validate", errStore("automation edit count is invalid")
	}
	if err := s.requireCleanCheckout(); err != nil {
		return autoApplyResult{}, nil, "validate", err
	}
	baseHead, err := s.gitHead()
	if err != nil {
		return autoApplyResult{}, nil, "validate", err
	}
	branch, err := s.branch()
	if err != nil {
		return autoApplyResult{}, nil, "validate", err
	}
	remoteURL, err := s.verifyRemote(s.configuredRemote())
	if err != nil {
		return autoApplyResult{}, nil, "validate", err
	}
	remote, err := s.remoteHead(branch)
	if err != nil || remote != baseHead {
		return autoApplyResult{}, nil, "validate", errStore("checkout must match the remote before automation")
	}

	planned := make([]plannedAutoEdit, 0, len(edits))
	seenTargets := make(map[string]struct{}, len(edits))
	targets := make(map[string]struct{}, len(edits))
	totalBytes := 0
	totalDiff := 0
	for _, edit := range edits {
		target, err := s.validateTarget(edit.Target, true)
		if err != nil || !strings.HasSuffix(strings.ToLower(target), ".md") {
			return autoApplyResult{}, nil, "validate", errStore("automation target is invalid")
		}
		if _, duplicate := seenTargets[target]; duplicate {
			return autoApplyResult{}, nil, "validate", errStore("automation targets must be unique")
		}
		seenTargets[target] = struct{}{}
		if _, ok := claimTypes[edit.ClaimType]; !ok {
			return autoApplyResult{}, nil, "validate", errStore("automation claim type is invalid")
		}
		if len([]rune(edit.Reason)) > maxTextRunes || len([]rune(edit.Source)) > maxTextRunes {
			return autoApplyResult{}, nil, "validate", errStore("automation explanation is too large")
		}
		if secretMaterialRE.MatchString(edit.Source) || secretMaterialRE.MatchString(edit.Reason) {
			return autoApplyResult{}, nil, "validate", errStore("automation explanation contains sensitive material")
		}
		if edit.Content == "" || len([]byte(edit.Content)) > maxAutoContentBytes || strings.IndexByte(edit.Content, 0) >= 0 || !utf8Valid([]byte(edit.Content)) {
			return autoApplyResult{}, nil, "validate", errStore("automation content is invalid")
		}
		if contentDeclaresSensitive(edit.Content) || secretMaterialRE.MatchString(edit.Content) {
			return autoApplyResult{}, nil, "validate", errStore("automation content contains sensitive material")
		}
		totalBytes += len([]byte(edit.Content))
		if totalBytes > maxAutoTotalBytes {
			return autoApplyResult{}, nil, "validate", errStore("automation content is too large")
		}
		if s.isSensitivePath(target) {
			return autoApplyResult{}, nil, "validate", errStore("automation target is sensitive")
		}
		targetPath := filepath.Join(s.repositoryDir, filepath.FromSlash(target))
		oldBytes, exists, err := readRegularFile(targetPath, maxAutoContentBytes)
		if err != nil {
			return autoApplyResult{}, nil, "validate", err
		}
		content := []byte(edit.Content)
		if bytesEqual(oldBytes, content) {
			continue
		}
		if (!exists || hasYAMLFrontmatter(string(oldBytes))) && !hasYAMLFrontmatter(edit.Content) {
			return autoApplyResult{}, nil, "validate", errStore("automation content must preserve YAML frontmatter")
		}
		diff := unifiedDiff(target, string(oldBytes), edit.Content)
		if len([]byte(diff)) > maxAutoDiffBytes {
			return autoApplyResult{}, nil, "validate", errStore("automation diff is too large")
		}
		totalDiff += len([]byte(diff))
		if totalDiff > maxAutoTotalDiff {
			return autoApplyResult{}, nil, "validate", errStore("automation diff is too large")
		}
		targets[target] = struct{}{}
		action := "update"
		if !exists {
			action = "create"
		}
		planned = append(planned, plannedAutoEdit{
			target: target, source: edit.Source, claimType: edit.ClaimType,
			reason: edit.Reason, content: content, old: oldBytes, exists: exists,
			action: action, diff: diff,
		})
	}
	if len(planned) == 0 {
		return autoApplyResult{}, []any{}, "done", nil
	}

	created := make([]string, 0, len(planned))
	committed := false
	rollback := func() {
		if committed {
			_, _ = s.runGit([]string{"reset", "--hard", baseHead}, s.repositoryDir, 2*time.Minute, nil)
		} else {
			_, _ = s.runGit([]string{"reset", "--hard", baseHead}, s.repositoryDir, 2*time.Minute, nil)
		}
		for _, path := range created {
			_ = os.Remove(path)
		}
	}
	success := false
	defer func() {
		if !success {
			rollback()
		}
	}()

	for _, edit := range planned {
		targetPath := filepath.Join(s.repositoryDir, filepath.FromSlash(edit.target))
		if err := writeAtomic(targetPath, edit.content, 0o644); err != nil {
			return autoApplyResult{}, nil, "commit", errStore("automation target could not be written")
		}
		if !edit.exists {
			created = append(created, targetPath)
		}
	}
	changed, err := s.statusPaths(false)
	if err != nil || !samePaths(changed, targets) {
		return autoApplyResult{}, nil, "commit", errStore("checkout contains an unreviewed change")
	}
	for _, edit := range planned {
		if _, err := s.runGit([]string{"add", "--", edit.target}, s.repositoryDir, time.Minute, nil); err != nil {
			return autoApplyResult{}, nil, "commit", errStore("automation could not be staged")
		}
	}
	staged, err := s.stagedPaths()
	if err != nil || !samePaths(staged, targets) {
		return autoApplyResult{}, nil, "commit", errStore("staged diff contains an unexpected path")
	}
	unstaged, err := s.statusPaths(true)
	if err != nil || len(unstaged) != 0 {
		return autoApplyResult{}, nil, "commit", errStore("checkout contains an unstaged change")
	}
	message := "folio: automatic context maintenance " + runID
	if _, err := s.runGit([]string{"commit", "--no-verify", "--no-gpg-sign", "-m", message}, s.repositoryDir, 2*time.Minute, []string{
		"user.name=Folio Service",
		"user.email=folio-service@users.noreply.github.com",
	}); err != nil {
		return autoApplyResult{}, nil, "commit", errStore("automation commit failed")
	}
	committed = true
	newHead, err := s.gitHead()
	if err != nil {
		return autoApplyResult{}, nil, "commit", err
	}
	committedPaths, err := s.commitPaths()
	if err != nil || !samePaths(committedPaths, targets) {
		return autoApplyResult{}, nil, "commit", errStore("commit contains an unexpected path")
	}
	if _, err := s.runGit([]string{"push", "--no-verify", remoteURL, "HEAD:refs/heads/" + branch}, s.repositoryDir, 5*time.Minute, nil); err != nil {
		remoteAfter, verifyErr := s.remoteHead(branch)
		if verifyErr != nil || remoteAfter != newHead {
			return autoApplyResult{}, nil, "push", errStore("push failed; the local commit was rolled back")
		}
	}
	remoteAfter, err := s.remoteHead(branch)
	if err != nil || remoteAfter != newHead {
		return autoApplyResult{}, nil, "push", errStore("push result could not be verified")
	}
	if err := s.recordRepositoryHead(newHead, branch); err != nil {
		return autoApplyResult{}, nil, "push", err
	}
	success = true
	audit := make([]any, 0, len(planned))
	for _, edit := range planned {
		audit = append(audit, map[string]any{
			"target": edit.target, "source": publicText(edit.source, maxTextRunes),
			"claim_type": edit.claimType, "reason": publicText(edit.reason, maxTextRunes),
			"action": edit.action, "old_sha256": nullableHash(edit.old, edit.exists),
			"new_sha256": sha256Hex(edit.content), "diff": publicText(edit.diff, maxAutoDiffBytes),
		})
	}
	return autoApplyResult{commit: newHead, branch: branch}, audit, "done", nil
}

func (s *Store) runAutoAgentLocked(runner, model string) (autoReport, error) {
	command, err := s.installedFolioCommand()
	if err != nil {
		return autoReport{}, err
	}
	reportPath := filepath.Join(s.runtimeDir, "auto-report.json")
	if info, statErr := os.Lstat(reportPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return autoReport{}, errStore("automation report path is invalid")
		}
		_ = os.Remove(reportPath)
	} else if !os.IsNotExist(statErr) {
		return autoReport{}, errStore("automation report path is invalid")
	}
	command = append(command, "agent", "--auto", "--device", "Docker", "--runtime-dir", s.runtimeDir, "--runner", runner, "--auto-report", reportPath, "--timeout", "1200")
	if model != "" {
		command = append(command, "--model", model)
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
	if _, err := s.runCommand(command, s.toolRoot, "", 20*time.Minute, true, env); err != nil {
		return autoReport{}, errStore("automation agent failed")
	}
	if info, statErr := os.Lstat(reportPath); statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxAutoRunBytes {
		return autoReport{}, errStore("automation report is unavailable")
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil || len(raw) > maxAutoRunBytes {
		return autoReport{}, errStore("automation report is unavailable")
	}
	_ = os.Remove(reportPath)
	var report autoReport
	if err := json.Unmarshal(raw, &report); err != nil || report.Status != "pending-apply" {
		return autoReport{}, errStore("automation report is invalid")
	}
	if len([]rune(report.Summary)) > maxAutoSummaryRunes || len(report.Edits) > maxAutoEdits || report.Skipped < 0 {
		return autoReport{}, errStore("automation report is invalid")
	}
	for _, edit := range report.Edits {
		if len([]rune(edit.Source)) > maxTextRunes || len([]rune(edit.Target)) > 512 || len([]rune(edit.Reason)) > maxTextRunes || len([]rune(edit.ClaimType)) > 32 || len([]byte(edit.Content)) > maxAutoContentBytes {
			return autoReport{}, errStore("automation report is invalid")
		}
	}
	return report, nil
}

func (s *Store) saveAutomationRun(run map[string]any) error {
	runID, _ := run["run_id"].(string)
	if !autoRunIDRE.MatchString(runID) {
		return errStore("automation run id is invalid")
	}
	if err := ensurePrivateDir(s.runsDir); err != nil {
		return err
	}
	raw, err := json.Marshal(run)
	if err != nil || len(raw) > maxAutoRunBytes {
		return errStore("automation run could not be stored")
	}
	if err := writeAtomic(filepath.Join(s.runsDir, runID+".json"), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	s.pruneAutomationRuns()
	return nil
}

func (s *Store) pruneAutomationRuns() {
	entries, err := os.ReadDir(s.runsDir)
	if err != nil {
		return
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	if len(files) > maxAutoRuns {
		for _, name := range files[maxAutoRuns:] {
			_ = os.Remove(filepath.Join(s.runsDir, name))
		}
	}
}

func (s *Store) recordSchedulerRun(run map[string]any) error {
	status, _ := run["status"].(string)
	finished, _ := run["finished_at"].(string)
	runID, _ := run["run_id"].(string)
	trigger, _ := run["trigger"].(string)
	saved := s.loadState()
	if trigger == "scheduler" {
		saved["scheduler"] = map[string]any{
			"status": status, "finished_at": finished, "last_run_id": runID, "trigger": trigger,
		}
	}
	saved["maintenance"] = map[string]any{
		"status": status, "mode": "auto", "finished_at": finished, "runner": run["runner"],
	}
	return s.saveState(saved)
}

// ListAutomation reads immutable, atomically replaced run records without the
// repository lock. A long model call must not hide progress from the owner.
func (s *Store) ListAutomation() (map[string]any, error) {
	files, err := s.automationRunFiles()
	if err != nil {
		return nil, err
	}
	runs := make([]any, 0, len(files))
	var latest map[string]any
	for index, name := range files {
		value, loadErr := s.loadJSON(filepath.Join(s.runsDir, name), maxAutoRunBytes)
		if loadErr != nil {
			continue
		}
		run, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if index == 0 {
			latest = run
		}
		runs = append(runs, automationSummary(run))
	}
	return map[string]any{"runs": runs, "count": len(runs), "latest": latest}, nil
}

// AutomationRun reads one immutable run record without the repository lock.
func (s *Store) AutomationRun(runID string) (map[string]any, error) {
	if !autoRunIDRE.MatchString(runID) {
		return nil, errStore("automation run id is invalid")
	}
	value, err := s.loadJSON(filepath.Join(s.runsDir, runID+".json"), maxAutoRunBytes)
	if err != nil {
		return nil, errStore("automation run is unavailable")
	}
	run, ok := value.(map[string]any)
	if !ok || responseString(run, "run_id") != runID {
		return nil, errStore("automation run is unavailable")
	}
	return run, nil
}

func (s *Store) automationRunFiles() ([]string, error) {
	if err := ensurePrivateDir(s.runsDir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.runsDir)
	if err != nil {
		return nil, errStore("automation runs are unavailable")
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if !autoRunIDRE.MatchString(strings.TrimSuffix(entry.Name(), ".json")) {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	if len(files) > maxAutoRuns {
		files = files[:maxAutoRuns]
	}
	return files, nil
}

func automationSummary(run map[string]any) map[string]any {
	edits, _ := run["edits"].([]any)
	targets := make([]any, 0, len(edits))
	for _, item := range edits {
		edit, ok := item.(map[string]any)
		if !ok {
			continue
		}
		targets = append(targets, map[string]any{
			"target": publicText(edit["target"], 512),
			"action": publicText(edit["action"], 16),
		})
	}
	return map[string]any{
		"run_id": publicText(run["run_id"], 64), "trigger": publicText(run["trigger"], 16),
		"status": publicText(run["status"], 32), "stage": publicText(run["stage"], 32),
		"runner": publicText(run["runner"], 16), "model": publicText(run["model"], 200),
		"started_at": publicText(run["started_at"], 64), "finished_at": publicText(run["finished_at"], 64),
		"summary": publicText(run["summary"], maxAutoSummaryRunes), "reason": publicText(run["reason"], 128),
		"commit": publicText(run["commit"], 40), "files_changed": len(edits), "files": targets,
	}
}

func automationPullSummary(pull map[string]any) map[string]any {
	return map[string]any{
		"status": publicText(pull["status"], 32),
		"head":   validSHA(pull["head"]),
		"remote": validSHA(pull["remote"]),
	}
}

func hasYAMLFrontmatter(text string) bool {
	text = strings.TrimPrefix(text, "\ufeff")
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return false
	}
	if strings.HasPrefix(text, "---\r\n") {
		text = text[len("---\r\n"):]
	} else {
		text = text[len("---\n"):]
	}
	index := strings.Index(text, "\n---\n")
	if index < 0 {
		index = strings.Index(text, "\n---\r\n")
	}
	return index > 0
}

func automationErrorCode(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "target"):
		return "target_invalid"
	case strings.Contains(message, "content") || strings.Contains(message, "sensitive") || strings.Contains(message, "material"):
		return "content_rejected"
	case strings.Contains(message, "diff"):
		return "diff_rejected"
	case strings.Contains(message, "staged") || strings.Contains(message, "unreviewed") || strings.Contains(message, "unstaged"):
		return "checkout_rejected"
	case strings.Contains(message, "commit"):
		return "commit_failed"
	case strings.Contains(message, "push"):
		return "push_failed"
	default:
		return "validation_failed"
	}
}

func newAutoRunID() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random), nil
}

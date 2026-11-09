package store

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ReportResult accepts an audit run ID only. Neither HTTP callers nor agents
// can select a filesystem path. Historical task-named artifacts could be
// overwritten by retries; only their immutable run audit snapshot is returned.
func (s *Store) ReportResult(runID string) (map[string]any, error) {
	unavailable := func() (map[string]any, error) { return nil, errStore("report_result_unavailable") }
	if !autoRunIDRE.MatchString(runID) {
		return unavailable()
	}
	raw, err := readReportRegular(s.runsDir, runID+".json", maxAutoRunBytes)
	if err != nil {
		return unavailable()
	}
	var run map[string]any
	if json.Unmarshal(raw, &run) != nil || responseString(run, "run_id") != runID || responseString(run, "status") != "reported" && responseString(run, "status") != "completed" {
		return unavailable()
	}
	name := responseString(run, "report_file")
	taskID := responseString(run, "task_id")
	historical := false
	if name == "run-"+runID+".md" {
		raw, err = readReportRegular(s.reportsDir, name, maxReportTextBytes+1)
	} else if taskIDRE.MatchString(taskID) && (name == "task-"+taskID+".md" || name == "action-"+taskID+".md") {
		// Older task-named files were mutable across retries. Returning those
		// bytes would falsely attribute a later attempt to this earlier run.
		historical = true
		raw = []byte(responseString(run, "report_text"))
	} else {
		return unavailable()
	}
	text := strings.TrimSpace(string(raw))
	if err != nil || text == "" || len(text) > maxReportTextBytes || !utf8.ValidString(text) || secretMaterialRE.MatchString(text) {
		return unavailable()
	}
	result := map[string]any{"run_id": runID, "format": "markdown", "text": text}
	if historical {
		result["historical_snapshot"] = true
		result["truncated"] = true // Old audits do not record completeness.
	}
	if taskIDRE.MatchString(taskID) {
		result["task_id"] = taskID
	}
	return result, nil
}

// OpenRoot confines lookup even if a path changes during the read; SameFile
// rejects replacement between lstat and open. Reject symlinked roots/parents,
// symlink files, directories, devices, and excessive content before returning it.
func readReportRegular(dir, name string, limit int64) ([]byte, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errStore("report_result_unavailable")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != filepath.Clean(dir) {
		return nil, errStore("report_result_unavailable")
	}
	info, err := os.Lstat(filepath.Join(dir, name))
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errStore("report_result_unavailable")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errStore("report_result_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errStore("report_result_unavailable")
	}
	return raw, nil
}

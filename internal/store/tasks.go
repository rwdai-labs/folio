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

// Task states. A task is the durable record of work the owner wants done; a run
// is one attempt at it. Keeping them apart is what makes pause, deferral, and
// "run again" possible without touching the run ledger.
const (
	taskStatusQueued   = "queued"
	taskStatusRunning  = "running"
	taskStatusDone     = "done"
	taskStatusFailed   = "failed"
	taskStatusCanceled = "canceled"

	taskKindMaintenance = "maintenance"

	maxTasks           = 100
	maxTaskTitleRunes  = 120
	maxTaskSourceRunes = 96
	maxTaskReasonRunes = 200
	maxTasksFileBytes  = 256 * 1024
	maxPoliciesBytes   = 64 * 1024
)

var (
	taskIDRE   = regexp.MustCompile(`^t-[0-9a-f]{12}$`)
	taskKindRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type taskRecord struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Priority   string `json:"priority"`
	Source     string `json:"source"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	Attempts   int    `json:"attempts"`
	RunID      string `json:"run_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
	NotBefore  string `json:"not_before,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type tasksFile struct {
	Version int          `json:"version"`
	Paused  bool         `json:"paused"`
	Tasks   []taskRecord `json:"tasks"`
}

// taskWindow is the off-peak window in which deferrable work is allowed to
// start. Times are local to the resolved zone; a window may cross midnight.
type taskWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
	// OffsetMinutes pins the zone without depending on container tzdata. When
	// absent the server's local zone is used.
	OffsetMinutes *int `json:"utc_offset_minutes,omitempty"`
}

type taskPolicy struct {
	Defer  map[string]bool `json:"defer"`
	Window taskWindow      `json:"window"`
}

func defaultTaskPolicy() taskPolicy {
	return taskPolicy{
		Defer:  map[string]bool{"P0": false, "P1": false, "P2": true, "P3": true},
		Window: taskWindow{Start: "00:30", End: "07:30"},
	}
}

func validPriority(priority string) bool {
	switch priority {
	case "P0", "P1", "P2", "P3":
		return true
	}
	return false
}

func priorityRank(priority string) int {
	switch priority {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P3":
		return 3
	}
	return 2
}

// loadTaskPolicy reads policies.json, filling anything missing with defaults so
// a partial file cannot disable deferral by accident.
func (s *Store) loadTaskPolicy() taskPolicy {
	policy := defaultTaskPolicy()
	value, err := s.loadJSON(s.policyPath, maxPoliciesBytes)
	if err != nil {
		return policy
	}
	root, ok := value.(map[string]any)
	if !ok {
		return policy
	}
	if raw, ok := root["defer"].(map[string]any); ok {
		for key, item := range raw {
			if validPriority(key) {
				if flag, ok := item.(bool); ok {
					policy.Defer[key] = flag
				}
			}
		}
	}
	if raw, ok := root["window"].(map[string]any); ok {
		if start := responseString(raw, "start"); start != "" {
			policy.Window.Start = start
		}
		if end := responseString(raw, "end"); end != "" {
			policy.Window.End = end
		}
		if offset, ok := raw["utc_offset_minutes"].(float64); ok && offset >= -12*60 && offset <= 14*60 {
			minutes := int(offset)
			policy.Window.OffsetMinutes = &minutes
		}
	}
	return policy
}

func parseClock(raw string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, errHour := time.Parse("15", parts[0])
	minute, errMinute := time.Parse("04", parts[1])
	if errHour != nil || errMinute != nil {
		return 0, false
	}
	return hour.Hour()*60 + minute.Minute(), true
}

func (w taskWindow) location(now time.Time) *time.Location {
	if w.OffsetMinutes != nil {
		return time.FixedZone("policy", *w.OffsetMinutes*60)
	}
	return now.Location()
}

// bounds returns today's window in the resolved zone. A window whose end is not
// after its start crosses midnight.
func (w taskWindow) bounds(now time.Time) (time.Time, time.Time, bool) {
	startMin, okStart := parseClock(w.Start)
	endMin, okEnd := parseClock(w.End)
	if !okStart || !okEnd {
		return time.Time{}, time.Time{}, false
	}
	location := w.location(now)
	local := now.In(location)
	start := time.Date(local.Year(), local.Month(), local.Day(), startMin/60, startMin%60, 0, 0, location)
	end := time.Date(local.Year(), local.Month(), local.Day(), endMin/60, endMin%60, 0, 0, location)
	if !end.After(start) {
		end = end.Add(24 * time.Hour)
	}
	return start, end, true
}

// inWindow reports whether now falls inside the window, including the tail of
// yesterday's window when it crosses midnight.
func (w taskWindow) inWindow(now time.Time) bool {
	for _, day := range []int{0, -1} {
		start, end, ok := w.bounds(now.AddDate(0, 0, day))
		if !ok {
			return false
		}
		if !now.Before(start) && now.Before(end) {
			return true
		}
	}
	return false
}

// nextStart is when deferred work becomes eligible. Inside the window that is
// now; outside it is the next opening, never a guessed duration.
func (w taskWindow) nextStart(now time.Time) time.Time {
	if w.inWindow(now) {
		return now
	}
	start, _, ok := w.bounds(now)
	if !ok || !start.After(now) {
		start, _, ok = w.bounds(now.AddDate(0, 0, 1))
		if !ok {
			return now
		}
	}
	return start
}

func newTaskID() (string, error) {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return "", errStore("task id could not be generated")
	}
	return "t-" + hex.EncodeToString(buffer), nil
}

func (s *Store) loadTasksLocked() (*tasksFile, error) {
	if s.tasksCache != nil {
		return s.tasksCache, nil
	}
	file := &tasksFile{Version: 1, Tasks: []taskRecord{}}
	if _, statErr := os.Lstat(s.tasksPath); statErr != nil {
		if os.IsNotExist(statErr) {
			s.tasksCache = file
			return file, nil
		}
		return nil, errStore("tasks are unavailable")
	}
	value, err := s.loadJSON(s.tasksPath, maxTasksFileBytes)
	if err == nil {
		raw, marshalErr := json.Marshal(value)
		if marshalErr == nil {
			var loaded tasksFile
			if json.Unmarshal(raw, &loaded) == nil {
				file.Paused = loaded.Paused
				for _, task := range loaded.Tasks {
					if validTaskRecord(task) {
						file.Tasks = append(file.Tasks, task)
					}
				}
			}
		}
	} else {
		return nil, errStore("tasks are unavailable")
	}
	s.tasksCache = file
	return file, nil
}

func validTaskRecord(task taskRecord) bool {
	if !taskIDRE.MatchString(task.ID) || !taskKindRE.MatchString(task.Kind) || !validPriority(task.Priority) {
		return false
	}
	switch task.Status {
	case taskStatusQueued, taskStatusRunning, taskStatusDone, taskStatusFailed, taskStatusCanceled:
	default:
		return false
	}
	if len([]rune(task.Title)) > maxTaskTitleRunes || len([]rune(task.Source)) > maxTaskSourceRunes || len([]rune(task.Reason)) > maxTaskReasonRunes {
		return false
	}
	if task.Attempts < 0 || task.Attempts > 100 {
		return false
	}
	for _, raw := range []string{task.CreatedAt, task.UpdatedAt, task.NotBefore, task.FinishedAt} {
		if raw == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
			return false
		}
	}
	return true
}

func (s *Store) saveTasksLocked(file *tasksFile) error {
	if len(file.Tasks) > maxTasks {
		file.Tasks = pruneTasks(file.Tasks)
	}
	raw, err := json.Marshal(file)
	if err != nil || len(raw) > maxTasksFileBytes {
		return errStore("tasks could not be stored")
	}
	if err := writeAtomic(s.tasksPath, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	s.tasksCache = file
	return nil
}

// pruneTasks keeps active work and the newest finished records.
func pruneTasks(tasks []taskRecord) []taskRecord {
	active := make([]taskRecord, 0, len(tasks))
	finished := make([]taskRecord, 0, len(tasks))
	for _, task := range tasks {
		if task.Status == taskStatusQueued || task.Status == taskStatusRunning {
			active = append(active, task)
			continue
		}
		finished = append(finished, task)
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].UpdatedAt > finished[j].UpdatedAt })
	keep := maxTasks - len(active)
	if keep < 0 {
		keep = 0
	}
	if len(finished) > keep {
		finished = finished[:keep]
	}
	return append(active, finished...)
}

func (s *Store) recoverInterruptedTasks() {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	file, err := s.loadTasksLocked()
	if err != nil {
		return
	}
	changed := false
	for index := range file.Tasks {
		task := &file.Tasks[index]
		if task.Status != taskStatusRunning {
			continue
		}
		task.Status = taskStatusFailed
		task.Reason = "interrupted"
		task.FinishedAt = nowString()
		task.UpdatedAt = nowString()
		changed = true
	}
	if changed {
		_ = s.saveTasksLocked(file)
	}
}

// CreateTask records work to do. Priority decides whether it may start now or
// waits for the off-peak window. An identical active task is returned instead of
// creating a duplicate, so a noisy event source cannot flood the queue.
func (s *Store) CreateTask(kind, source, title, priority string) (map[string]any, error) {
	kind = strings.TrimSpace(kind)
	source = strings.TrimSpace(source)
	title = strings.TrimSpace(title)
	if !taskKindRE.MatchString(kind) {
		return nil, errStore("invalid_task_kind")
	}
	if priority == "" {
		priority = "P2"
	}
	if !validPriority(priority) {
		return nil, errStore("invalid_task_priority")
	}
	if len([]rune(title)) > maxTaskTitleRunes || len([]rune(source)) > maxTaskSourceRunes {
		return nil, errStore("invalid_task")
	}
	now := time.Now()
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	file, err := s.loadTasksLocked()
	if err != nil {
		return nil, err
	}
	for _, task := range file.Tasks {
		if task.Kind == kind && task.Source == source && (task.Status == taskStatusQueued || task.Status == taskStatusRunning) {
			return map[string]any{"task": publicTask(task), "created": false}, nil
		}
	}
	id, err := newTaskID()
	if err != nil {
		return nil, err
	}
	policy := s.loadTaskPolicy()
	task := taskRecord{
		ID: id, Kind: kind, Title: title, Priority: priority, Source: source,
		Status: taskStatusQueued, Enabled: true,
		CreatedAt: now.UTC().Format(time.RFC3339Nano),
		UpdatedAt: now.UTC().Format(time.RFC3339Nano),
	}
	if policy.Defer[priority] {
		task.NotBefore = policy.Window.nextStart(now).UTC().Format(time.RFC3339Nano)
	}
	file.Tasks = append(file.Tasks, task)
	if err := s.saveTasksLocked(file); err != nil {
		return nil, err
	}
	return map[string]any{"task": publicTask(task), "created": true}, nil
}

func publicTask(task taskRecord) map[string]any {
	result := map[string]any{
		"id": task.ID, "kind": task.Kind, "title": task.Title, "priority": task.Priority,
		"source": task.Source, "status": task.Status, "enabled": task.Enabled,
		"attempts": task.Attempts, "created_at": task.CreatedAt, "updated_at": task.UpdatedAt,
	}
	if task.RunID != "" {
		result["run_id"] = task.RunID
	}
	if task.Reason != "" {
		result["reason"] = task.Reason
	}
	if task.NotBefore != "" {
		result["not_before"] = task.NotBefore
	}
	if task.FinishedAt != "" {
		result["finished_at"] = task.FinishedAt
	}
	return result
}

// ListTasks reports the queue, the pause switch, and the policy that decides
// what may start now.
func (s *Store) ListTasks() (map[string]any, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	return s.listTasksLocked()
}

// listTasksLocked builds the listing; the caller holds taskMu.
func (s *Store) listTasksLocked() (map[string]any, error) {
	file, err := s.loadTasksLocked()
	if err != nil {
		return nil, err
	}
	policy := s.loadTaskPolicy()
	now := time.Now()
	tasks := make([]taskRecord, len(file.Tasks))
	copy(tasks, file.Tasks)
	sort.SliceStable(tasks, func(i, j int) bool {
		activeI := tasks[i].Status == taskStatusQueued || tasks[i].Status == taskStatusRunning
		activeJ := tasks[j].Status == taskStatusQueued || tasks[j].Status == taskStatusRunning
		if activeI != activeJ {
			return activeI
		}
		if priorityRank(tasks[i].Priority) != priorityRank(tasks[j].Priority) {
			return priorityRank(tasks[i].Priority) < priorityRank(tasks[j].Priority)
		}
		return tasks[i].CreatedAt < tasks[j].CreatedAt
	})
	items := make([]any, 0, len(tasks))
	counts := map[string]int{}
	for _, task := range tasks {
		entry := publicTask(task)
		entry["due"] = taskDue(task, now)
		items = append(items, entry)
		counts[task.Status]++
	}
	return map[string]any{
		"paused": file.Paused,
		"policy": map[string]any{
			"defer": policy.Defer, "window_start": policy.Window.Start,
			"window_end": policy.Window.End, "in_window": policy.Window.inWindow(now),
		},
		"counts": counts,
		"tasks":  items,
	}, nil
}

func taskDue(task taskRecord, now time.Time) bool {
	if !task.Enabled || task.Status != taskStatusQueued {
		return false
	}
	if task.NotBefore == "" {
		return true
	}
	notBefore, err := time.Parse(time.RFC3339Nano, task.NotBefore)
	if err != nil {
		return true
	}
	return !now.Before(notBefore)
}

// SetTasksPaused flips the global switch. Paused means "keep queuing, start
// nothing"; it never drops work.
func (s *Store) SetTasksPaused(paused bool) (map[string]any, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	file, err := s.loadTasksLocked()
	if err != nil {
		return nil, err
	}
	file.Paused = paused
	if err := s.saveTasksLocked(file); err != nil {
		return nil, err
	}
	return s.listTasksLocked()
}

// UpdateTask toggles one task. Pausing a queued task keeps its place; resuming
// respects any deferral that is still in the future.
func (s *Store) UpdateTask(id string, enabled bool) (map[string]any, error) {
	if !taskIDRE.MatchString(id) {
		return nil, errStore("task_not_found")
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	file, err := s.loadTasksLocked()
	if err != nil {
		return nil, err
	}
	index := -1
	for i := range file.Tasks {
		if file.Tasks[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, errStore("task_not_found")
	}
	if file.Tasks[index].Status == taskStatusQueued {
		file.Tasks[index].Enabled = enabled
		file.Tasks[index].UpdatedAt = nowString()
		if err := s.saveTasksLocked(file); err != nil {
			return nil, err
		}
	}
	return s.listTasksLocked()
}

// RunTaskNow clears deferral and re-arms a finished task. It is the owner's
// override: the window gate does not apply to an explicit request.
func (s *Store) RunTaskNow(id string) (map[string]any, error) {
	if !taskIDRE.MatchString(id) {
		return nil, errStore("task_not_found")
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	file, err := s.loadTasksLocked()
	if err != nil {
		return nil, err
	}
	index := -1
	for i := range file.Tasks {
		if file.Tasks[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, errStore("task_not_found")
	}
	task := &file.Tasks[index]
	if task.Status == taskStatusRunning {
		return nil, errStore("task_busy")
	}
	if task.Status == taskStatusCanceled {
		return nil, errStore("task_canceled")
	}
	task.Status = taskStatusQueued
	task.Enabled = true
	task.NotBefore = ""
	task.Reason = ""
	task.FinishedAt = ""
	task.UpdatedAt = nowString()
	if err := s.saveTasksLocked(file); err != nil {
		return nil, err
	}
	return s.listTasksLocked()
}

// CancelTask retires a queued task. A running task cannot be canceled mid-call;
// the dispatcher would have no safe point to stop at.
func (s *Store) CancelTask(id string) (map[string]any, error) {
	if !taskIDRE.MatchString(id) {
		return nil, errStore("task_not_found")
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	file, err := s.loadTasksLocked()
	if err != nil {
		return nil, err
	}
	index := -1
	for i := range file.Tasks {
		if file.Tasks[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, errStore("task_not_found")
	}
	task := &file.Tasks[index]
	if task.Status == taskStatusRunning {
		return nil, errStore("task_busy")
	}
	task.Status = taskStatusCanceled
	task.UpdatedAt = nowString()
	task.FinishedAt = nowString()
	if err := s.saveTasksLocked(file); err != nil {
		return nil, err
	}
	return s.listTasksLocked()
}

// DispatchDue starts at most one due task. Serialization is the product
// decision, not a temporary limit: one model call at a time keeps the checkout,
// the commit, and the audit trail unambiguous.
func (s *Store) DispatchDue() (map[string]any, error) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()

	s.taskMu.Lock()
	file, err := s.loadTasksLocked()
	if err != nil {
		s.taskMu.Unlock()
		return nil, err
	}
	if file.Paused {
		s.taskMu.Unlock()
		return map[string]any{"dispatched": 0, "reason": "paused"}, nil
	}
	now := time.Now()
	index := -1
	for i := range file.Tasks {
		task := file.Tasks[i]
		if !taskDue(task, now) {
			continue
		}
		if index < 0 || priorityRank(task.Priority) < priorityRank(file.Tasks[index].Priority) ||
			(priorityRank(task.Priority) == priorityRank(file.Tasks[index].Priority) && task.CreatedAt < file.Tasks[index].CreatedAt) {
			index = i
		}
	}
	if index < 0 {
		s.taskMu.Unlock()
		return map[string]any{"dispatched": 0}, nil
	}
	task := &file.Tasks[index]
	if task.Kind != taskKindMaintenance {
		// A task whose kind has no implementation must not spin forever.
		task.Status = taskStatusFailed
		task.Reason = "unsupported_kind"
		task.FinishedAt = nowString()
		task.UpdatedAt = nowString()
		_ = s.saveTasksLocked(file)
		s.taskMu.Unlock()
		return map[string]any{"dispatched": 0, "reason": "unsupported_kind"}, nil
	}
	task.Status = taskStatusRunning
	task.Attempts++
	task.UpdatedAt = nowString()
	if err := s.saveTasksLocked(file); err != nil {
		s.taskMu.Unlock()
		return nil, err
	}
	taskID := task.ID
	s.taskMu.Unlock()

	run, runErr := s.RunAutomation("task", "", "codex")
	result := map[string]any{"dispatched": 1, "task_id": taskID}

	s.taskMu.Lock()
	file, loadErr := s.loadTasksLocked()
	if loadErr != nil {
		s.taskMu.Unlock()
		return nil, loadErr
	}
	for i := range file.Tasks {
		if file.Tasks[i].ID != taskID {
			continue
		}
		item := &file.Tasks[i]
		item.UpdatedAt = nowString()
		item.FinishedAt = nowString()
		if runErr != nil {
			item.Status = taskStatusFailed
			item.Reason = "dispatch_failed"
			break
		}
		if runID := responseString(run, "run_id"); runID != "" {
			item.RunID = runID
		}
		status := responseString(run, "status")
		switch status {
		case "pushed", "no_changes":
			item.Status = taskStatusDone
		default:
			item.Status = taskStatusFailed
			item.Reason = responseString(run, "reason")
			if item.Reason == "" {
				item.Reason = status
			}
		}
		result["run_status"] = status
		break
	}
	if err := s.saveTasksLocked(file); err != nil {
		s.taskMu.Unlock()
		return nil, err
	}
	s.taskMu.Unlock()
	if runErr != nil {
		return result, nil
	}
	return result, nil
}

// policyPathFor is the private policy file, next to the other runtime data.
func policyPathFor(data string) string { return filepath.Join(data, "policies.json") }

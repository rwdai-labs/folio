// Package contextrepo contains the deterministic, repository-owned context
// router and the proposal-only maintenance worker.
package contextrepo

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	dateFormat     = "2006-01-02"
	defaultRuntime = ".local/share/personal-context"
)

var (
	canonicalDirs = []string{"core", "active", "domains", "projects", "decisions", "knowledge", "history"}
	termPattern   = regexp.MustCompile(`(?i)[a-z0-9][a-z0-9_-]*`)
)

// Metadata contains the intentionally small YAML subset used by context
// documents. Values are strings, booleans, nil, or []any of those values.
type Metadata map[string]any

// Document is a regular Markdown document inside the configured checkout.
type Document struct {
	Path     string
	Metadata Metadata
	Body     string
}

// SelectedSource is one document in a generated packet.
type SelectedSource struct {
	Document
	Reason string
}

// Router reads the canonical checkout selected by trusted configuration.
// Root is resolved once during construction; task and device strings cannot
// change the checkout being read.
type Router struct {
	Root string
	Now  func() time.Time
}

// New creates a router for an explicit repository root.
func New(root string) (*Router, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("repository root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat repository root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("repository root is not a directory")
	}
	return &Router{Root: filepath.Clean(resolved), Now: time.Now}, nil
}

// NewFromEnv resolves the checkout from PERSONAL_CONTEXT_ROOT. The current
// working directory is the local CLI fallback when the trusted setting is
// absent; model-facing inputs never participate in root selection.
func NewFromEnv() (*Router, error) {
	root := strings.TrimSpace(os.Getenv("PERSONAL_CONTEXT_ROOT"))
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("determine repository root: %w", err)
		}
	}
	return New(root)
}

func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Router) inside(path string, allowRoot bool) (string, error) {
	if r == nil || r.Root == "" {
		return "", errors.New("router has no repository root")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Root, path)
	}
	path = filepath.Clean(path)
	rel, err := filepath.Rel(r.Root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes repository root")
	}
	if !allowRoot && rel == "." {
		return "", errors.New("repository root is not a file")
	}
	return path, nil
}

func (r *Router) safePath(path string, allowRoot bool) (string, error) {
	path, err := r.inside(path, allowRoot)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(r.Root)
	if err != nil {
		return "", err
	}
	if _, err := pathWithin(resolvedRoot, resolved, allowRoot); err != nil {
		return "", err
	}
	return path, nil
}

func pathWithin(root, path string, allowRoot bool) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes repository root")
	}
	if !allowRoot && rel == "." {
		return "", errors.New("repository root is not a file")
	}
	return rel, nil
}

func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("symlink is not a context document")
	}
	if !info.Mode().IsRegular() {
		return errors.New("context document is not a regular file")
	}
	return nil
}

// ReadDoc reads one explicitly selected document. Symlinks and paths outside
// the checkout are rejected even when their target points back inside.
func (r *Router) ReadDoc(path string) (Document, Metadata, error) {
	full, err := r.safePath(path, false)
	if err != nil {
		return Document{}, nil, err
	}
	if err := regularFile(full); err != nil {
		return Document{}, nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return Document{}, nil, err
	}
	meta, body := parseDocument(string(data))
	rel, err := r.relative(full)
	if err != nil {
		return Document{}, nil, err
	}
	doc := Document{Path: rel, Metadata: meta, Body: body}
	return doc, meta, nil
}

func parseDocument(text string) (Metadata, string) {
	meta := Metadata{}
	body := text
	if strings.HasPrefix(text, "---\n") {
		rest := text[len("---\n"):]
		if end := frontMatterEnd(rest); end >= 0 {
			raw := rest[:end]
			for _, line := range strings.Split(raw, "\n") {
				line = strings.TrimSuffix(line, "\r")
				if strings.HasPrefix(line, " ") || !strings.Contains(line, ":") {
					continue
				}
				key, value, _ := strings.Cut(line, ":")
				key = strings.TrimSpace(key)
				if key == "" {
					continue
				}
				meta[key] = parseValue(value)
			}
			body = rest[end+len("\n---"):]
		}
	}
	return meta, strings.TrimSpace(body)
}

func frontMatterEnd(text string) int {
	for offset := 0; offset <= len(text); {
		next := strings.IndexByte(text[offset:], '\n')
		lineEnd := len(text)
		if next >= 0 {
			lineEnd = offset + next
		}
		line := strings.TrimSuffix(text[offset:lineEnd], "\r")
		if line == "---" {
			return offset - 1
		}
		if next < 0 {
			break
		}
		offset = lineEnd + 1
	}
	return -1
}

func parseValue(value string) any {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		inner := strings.TrimSpace(value[1 : len(value)-1])
		if inner == "" {
			return []any{}
		}
		parts := strings.Split(inner, ",")
		result := make([]any, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				result = append(result, parseValue(part))
			}
		}
		return result
	}
	lower := strings.ToLower(value)
	switch lower {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}

func (r *Router) relative(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r.Root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes repository root")
	}
	return filepath.ToSlash(rel), nil
}

func (r *Router) Docs() ([]Document, error) {
	result := make([]Document, 0)
	for _, directory := range canonicalDirs {
		base := filepath.Join(r.Root, directory)
		info, err := os.Lstat(base)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(entry.Name()) != ".md" {
				return nil
			}
			if err := regularFile(path); err != nil {
				return nil
			}
			doc, _, err := r.ReadDoc(path)
			if err != nil {
				return err
			}
			status := strings.ToLower(metaString(doc.Metadata, "status"))
			if status == "archived" || status == "rejected" {
				return nil
			}
			result = append(result, doc)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (r *Router) CandidateFiles() ([]string, error) {
	inbox := filepath.Join(r.Root, "inbox")
	info, err := os.Lstat(inbox)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return []string{}, nil
	}
	entries, err := os.ReadDir(inbox)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		full := filepath.Join(inbox, entry.Name())
		if err := regularFile(full); err != nil {
			continue
		}
		doc, _, err := r.ReadDoc(full)
		if err != nil {
			return nil, err
		}
		if metaString(doc.Metadata, "type") == "candidate" && (metaString(doc.Metadata, "status") == "" || metaString(doc.Metadata, "status") == "pending-review") {
			result = append(result, doc.Path)
		}
	}
	sort.Strings(result)
	return result, nil
}

func metaString(meta Metadata, key string) string {
	value, ok := meta[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func asList(value any) []string {
	result := make([]string, 0)
	switch values := value.(type) {
	case []any:
		for _, item := range values {
			if item != nil {
				result = append(result, strings.ToLower(fmt.Sprint(item)))
			}
		}
	case []string:
		for _, item := range values {
			if item != "" {
				result = append(result, strings.ToLower(item))
			}
		}
	case string:
		if values != "" {
			result = append(result, strings.ToLower(values))
		}
	default:
		if value != nil {
			result = append(result, strings.ToLower(fmt.Sprint(value)))
		}
	}
	return result
}

func depth(meta Metadata) int {
	value, err := strconv.Atoi(strings.TrimSpace(metaString(meta, "depth")))
	if err != nil {
		return 0
	}
	return value
}

func parentOf(meta Metadata) string {
	parent := strings.TrimSpace(metaString(meta, "parent"))
	if parent == "" || parent == "null" {
		return ""
	}
	return filepath.ToSlash(parent)
}

func layer(meta Metadata) string {
	return strings.ToUpper(strings.TrimSpace(metaString(meta, "layer")))
}

func sensitive(meta Metadata) bool {
	return strings.ToLower(strings.TrimSpace(metaString(meta, "sensitivity"))) == "sensitive"
}

func staleAt(meta Metadata, today time.Time) bool {
	value := strings.TrimSpace(metaString(meta, "review_after"))
	if value == "" {
		return false
	}
	reviewAfter, err := time.ParseInLocation(dateFormat, value, time.UTC)
	if err != nil {
		return false
	}
	today = today.UTC()
	return !reviewAfter.After(time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC))
}

// IsStale reports whether review_after is on or before the router's current
// UTC date. Invalid dates remain a non-stale signal, matching the Python CLI.
func (r *Router) IsStale(meta Metadata) bool { return staleAt(meta, r.now()) }

// Select applies the minimal L0 plus matching L1/L2 routing policy. Device is
// recorded by Packet but intentionally has no effect on selection.
func (r *Router) Select(task string, includeHistory, includeSensitive bool, _ string) ([]SelectedSource, error) {
	docs, err := r.Docs()
	if err != nil {
		return nil, err
	}
	available := make(map[string]Document, len(docs))
	for _, doc := range docs {
		available[doc.Path] = doc
	}
	terms := make(map[string]struct{})
	for _, term := range termPattern.FindAllString(strings.ToLower(task), -1) {
		terms[term] = struct{}{}
	}
	selected := make([]SelectedSource, 0)
	selectedPaths := make(map[string]bool)
	for _, doc := range docs {
		if layer(doc.Metadata) == "L2" && !includeHistory {
			continue
		}
		if sensitive(doc.Metadata) && !includeSensitive {
			continue
		}
		matched := make([]string, 0)
		for _, term := range asList(doc.Metadata["triggers"]) {
			if _, ok := terms[term]; ok {
				matched = append(matched, term)
			}
		}
		for _, scope := range asList(doc.Metadata["scope"]) {
			if _, ok := terms[scope]; ok {
				matched = append(matched, scope)
			}
		}
		if boolValue(doc.Metadata["load_default"]) {
			selected = append(selected, SelectedSource{Document: doc, Reason: "default L0"})
			selectedPaths[doc.Path] = true
		} else if len(matched) > 0 {
			sort.Strings(matched)
			matched = uniqueStrings(matched)
			selected = append(selected, SelectedSource{Document: doc, Reason: "matched: " + strings.Join(matched, ", ")})
			selectedPaths[doc.Path] = true
		}
	}
	initial := append([]SelectedSource(nil), selected...)
	for _, item := range initial {
		parent := parentOf(item.Metadata)
		visited := map[string]bool{}
		for parent != "" && !visited[parent] {
			visited[parent] = true
			ancestor, ok := available[parent]
			if !ok {
				break
			}
			if !selectedPaths[parent] && (layer(ancestor.Metadata) != "L2" || includeHistory) && (!sensitive(ancestor.Metadata) || includeSensitive) {
				selected = append(selected, SelectedSource{Document: ancestor, Reason: "ancestor summary"})
				selectedPaths[parent] = true
			}
			parent = parentOf(ancestor.Metadata)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if depth(selected[i].Metadata) != depth(selected[j].Metadata) {
			return depth(selected[i].Metadata) < depth(selected[j].Metadata)
		}
		return selected[i].Path < selected[j].Path
	})
	return selected, nil
}

func boolValue(value any) bool {
	v, ok := value.(bool)
	return ok && v
}

func uniqueStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

// Summary retains the first useful heading/claim lines for ancestor display.
func Summary(body string) string {
	result := make([]string, 0, 3)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "Source:") {
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") || len(result) == 0 {
			result = append(result, line)
		}
		if len(result) >= 3 {
			break
		}
	}
	if len(result) == 0 {
		return "(summary unavailable)"
	}
	return strings.Join(result, "\n")
}

func formatSources(selected []SelectedSource, r *Router) []string {
	result := make([]string, 0, len(selected))
	for _, item := range selected {
		disclosure := "full"
		if item.Reason == "ancestor summary" {
			disclosure = "summary"
		}
		result = append(result, fmt.Sprintf("- `%s` (D%d, %s, %s, updated %s; %s; %s)", item.Path, depth(item.Metadata), valueOr(item.Metadata, "layer", "unknown"), valueOr(item.Metadata, "status", "unknown"), valueOr(item.Metadata, "updated", "unknown"), disclosure, item.Reason))
	}
	return result
}

func valueOr(meta Metadata, key, fallback string) string {
	if value := metaString(meta, key); value != "" {
		return value
	}
	return fallback
}

// Packet renders a temporary context packet. It never writes the checkout.
func (r *Router) Packet(task string, selected []SelectedSource, includeHistory, includeSensitive bool, device string) string {
	var b strings.Builder
	now := r.now().Truncate(time.Second).Format(time.RFC3339)
	fmt.Fprintf(&b, "# Context Packet\nGenerated: %s\nTask: %s\n", now, task)
	if device != "" {
		fmt.Fprintf(&b, "Execution environment: %s\n", device)
	}
	b.WriteString("Selection: deterministic keyword router\n\n## Selected Sources\n")
	if len(selected) == 0 {
		b.WriteString("- None\n")
	} else {
		for _, line := range formatSources(selected, r) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n## Context\n")
	for _, item := range selected {
		body := item.Body
		disclosure := "full"
		if item.Reason == "ancestor summary" {
			body = Summary(body)
			disclosure = "summary"
		}
		if r.IsStale(item.Metadata) {
			fmt.Fprintf(&b, "WARNING: `%s` reached review_after=%s; verify before treating it as current.\n", item.Path, valueOr(item.Metadata, "review_after", ""))
		}
		fmt.Fprintf(&b, "### Source: `%s`\nDisclosure: %s; depth: D%d; layer: %s; scope: %s\n\n%s\n\n", item.Path, disclosure, depth(item.Metadata), valueOr(item.Metadata, "layer", "unknown"), strings.Join(asList(item.Metadata["scope"]), ", "), body)
	}
	fmt.Fprintf(&b, "## Retrieval Notes\n- L0 loaded: default stable context\n- L1 matched: keyword and scope matches\n- L2 retrieved: %s\n- Sensitive documents: %s\n", yesNo(includeHistory), sensitiveNote(includeSensitive))
	return b.String()
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func sensitiveNote(value bool) string {
	if value {
		return "explicitly allowed for this build"
	}
	return "excluded by default"
}

type accessEvent struct {
	Timestamp  string         `json:"timestamp"`
	TaskDigest string         `json:"task_digest"`
	Sources    []accessSource `json:"sources"`
}

type accessSource struct {
	Path       string `json:"path"`
	Depth      int    `json:"depth"`
	Disclosure string `json:"disclosure"`
	Reason     string `json:"reason"`
}

// RecordAccess appends only path, depth, reason and a task digest to ignored
// local telemetry. Task text itself is never written.
func (r *Router) RecordAccess(task string, selected []SelectedSource) error {
	path, err := r.telemetryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(task))
	event := accessEvent{Timestamp: r.now().Truncate(time.Second).Format(time.RFC3339), TaskDigest: hex.EncodeToString(hash[:])[:16]}
	for _, item := range selected {
		disclosure := "full"
		if item.Reason == "ancestor summary" {
			disclosure = "summary"
		}
		event.Sources = append(event.Sources, accessSource{Path: item.Path, Depth: depth(item.Metadata), Disclosure: disclosure, Reason: item.Reason})
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}

func (r *Router) telemetryPath() (string, error) {
	if strings.TrimSpace(os.Getenv("PERSONAL_CONTEXT_RUNTIME")) == "" {
		return filepath.Join(r.Root, "generated", "usage", "access-log.jsonl"), nil
	}
	runtime, err := r.RuntimePath("")
	if err != nil {
		return "", err
	}
	return filepath.Join(runtime, "generated", "usage", "access-log.jsonl"), nil
}

type accessCount struct {
	Count int
	First string
	Last  string
}

func (r *Router) accessCounts() (map[string]accessCount, error) {
	counts := make(map[string]accessCount)
	path, err := r.telemetryPath()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return counts, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event accessEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		for _, source := range event.Sources {
			if source.Disclosure != "full" || source.Reason == "ancestor summary" || source.Path == "" {
				continue
			}
			item := counts[source.Path]
			item.Count++
			if item.First == "" || (event.Timestamp != "" && event.Timestamp < item.First) {
				item.First = event.Timestamp
			}
			if event.Timestamp > item.Last {
				item.Last = event.Timestamp
			}
			counts[source.Path] = item
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

type ResidualRecommendation struct {
	Child        string
	Parent       string
	FullAccesses int
	Last         string
}

func (r *Router) ResidualRecommendations(minAccesses int) ([]ResidualRecommendation, error) {
	if minAccesses < 0 {
		return nil, errors.New("min-accesses must be non-negative")
	}
	counts, err := r.accessCounts()
	if err != nil {
		return nil, err
	}
	docs, err := r.Docs()
	if err != nil {
		return nil, err
	}
	result := make([]ResidualRecommendation, 0)
	for _, doc := range docs {
		parent := parentOf(doc.Metadata)
		item := counts[doc.Path]
		if depth(doc.Metadata) <= 0 || parent == "" || item.Count < minAccesses {
			continue
		}
		result = append(result, ResidualRecommendation{Child: doc.Path, Parent: parent, FullAccesses: item.Count, Last: item.Last})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Child != result[j].Child {
			return result[i].Child < result[j].Child
		}
		return result[i].Parent < result[j].Parent
	})
	return result, nil
}

type GitStatus struct {
	Available bool
	Dirty     bool
	Lines     []string
}

func (r *Router) GitStatus() GitStatus {
	git, err := exec.LookPath("git")
	if err != nil {
		return GitStatus{Lines: []string{"git is not installed or not on PATH"}}
	}
	cmd := exec.Command(git, "--no-optional-locks", "status", "--short", "--branch", "--untracked-files=normal")
	cmd.Dir = r.Root
	output, err := cmd.Output()
	if err != nil {
		return GitStatus{Lines: []string{err.Error()}}
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = []string{"(no status output)"}
	}
	dirty := false
	for _, line := range lines {
		if !strings.HasPrefix(line, "##") {
			dirty = true
			break
		}
	}
	return GitStatus{Available: true, Dirty: dirty, Lines: lines}
}

// WriteReport writes a derived maintenance report. The canonical checkout is
// only read unless links --propose/--apply is explicitly requested.
func (r *Router) WriteReport(device string, minAccesses int, reportDir string) (string, error) {
	candidates, err := r.CandidateFiles()
	if err != nil {
		return "", err
	}
	docs, err := r.Docs()
	if err != nil {
		return "", err
	}
	stale := make([]Document, 0)
	for _, doc := range docs {
		if layer(doc.Metadata) == "L1" && r.IsStale(doc.Metadata) {
			stale = append(stale, doc)
		}
	}
	recommendations, err := r.ResidualRecommendations(minAccesses)
	if err != nil {
		return "", err
	}
	git := r.GitStatus()
	var b strings.Builder
	b.WriteString("# Context Maintenance Report\nMode: read-only\n")
	fmt.Fprintf(&b, "Repository: %s\n", r.Root)
	if device != "" {
		fmt.Fprintf(&b, "Device: %s\n", device)
	}
	fmt.Fprintf(&b, "\n## Pending inbox candidates (%d)\n", len(candidates))
	if len(candidates) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, path := range candidates {
			fmt.Fprintf(&b, "- %s\n", path)
		}
	}
	fmt.Fprintf(&b, "\n## Stale L1 documents (%d)\n", len(stale))
	if len(stale) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, doc := range stale {
			fmt.Fprintf(&b, "- %s | updated=%s | review_after=%s\n", doc.Path, valueOr(doc.Metadata, "updated", "?"), valueOr(doc.Metadata, "review_after", "?"))
		}
	}
	fmt.Fprintf(&b, "\n## Residual-link recommendations (%d; min_accesses=%d)\n", len(recommendations), minAccesses)
	if len(recommendations) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, item := range recommendations {
			fmt.Fprintf(&b, "- %s -> %s | full_accesses=%d | last=%s\n", item.Child, item.Parent, item.FullAccesses, item.Last)
		}
	}
	b.WriteString("\n## Git status\n")
	if !git.Available {
		for _, line := range git.Lines {
			fmt.Fprintf(&b, "- [unavailable] %s\n", line)
		}
	} else {
		for _, line := range git.Lines {
			fmt.Fprintf(&b, "- %s\n", line)
		}
		if !git.Dirty {
			b.WriteString("- clean\n")
		}
	}
	fmt.Fprintf(&b, "\nSummary: pending=%d; stale_l1=%d; residual_links=%d; git_dirty=%s\n", len(candidates), len(stale), len(recommendations), yesNo(git.Dirty))
	report := b.String()
	if reportDir == "" {
		return report, nil
	}
	dir, err := filepath.Abs(reportDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "maintenance-report.md")
	if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ApplyResidualCandidate performs the explicit, reviewed canonical link edit.
func (r *Router) ApplyResidualCandidate(candidateName string) error {
	candidate, err := r.safePath(candidateName, false)
	if err != nil {
		return errors.New("candidate must be inside inbox")
	}
	inbox := filepath.Join(r.Root, "inbox")
	resolvedInbox, err := filepath.EvalSymlinks(inbox)
	if err != nil {
		return err
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return err
	}
	if _, err := pathWithin(resolvedInbox, resolvedCandidate, false); err != nil {
		return errors.New("candidate must be inside inbox")
	}
	if err := regularFile(candidate); err != nil {
		return err
	}
	doc, meta, err := r.ReadDoc(candidate)
	if err != nil {
		return err
	}
	if metaString(meta, "type") != "candidate" || metaString(meta, "candidate_kind") != "residual-link" {
		return errors.New("candidate is not a residual-link proposal")
	}
	if metaString(meta, "status") != "approved" {
		return errors.New("candidate must be reviewed with status: approved before applying")
	}
	parent := metaString(meta, "parent")
	child := metaString(meta, "child")
	if parent == "" || child == "" {
		return errors.New("candidate is missing parent or child")
	}
	parentPath, err := r.safePath(parent, false)
	if err != nil {
		return errors.New("link targets must be inside repository")
	}
	childPath, err := r.safePath(child, false)
	if err != nil {
		return errors.New("link targets must be inside repository")
	}
	if err := regularFile(parentPath); err != nil {
		return errors.New("candidate points to a missing parent or child")
	}
	if err := regularFile(childPath); err != nil {
		return errors.New("candidate points to a missing parent or child")
	}
	parentText, err := os.ReadFile(parentPath)
	if err != nil {
		return err
	}
	parentRel, _ := r.relative(parentPath)
	childRel, _ := r.relative(childPath)
	relTarget, err := filepath.Rel(filepath.Dir(parentPath), childPath)
	if err != nil {
		return err
	}
	link := fmt.Sprintf("- Related detail: [%s](%s)", childRel, filepath.ToSlash(relTarget))
	if strings.Contains(string(parentText), link) {
		return nil
	}
	updated := strings.TrimRight(string(parentText), "\r\n") + "\n\n## Related detail\n\n" + link + "\n"
	if err := os.WriteFile(parentPath, []byte(updated), 0o600); err != nil {
		return err
	}
	_ = doc
	_ = parentRel
	return nil
}

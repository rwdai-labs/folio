package contextrepo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

func contentDigest(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func (r *Router) readMaintenanceState(runtime, path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	resolved, err := resolveExistingPath(path)
	if err != nil {
		return nil, errors.New("maintenance state path is invalid")
	}
	if _, err := pathWithin(runtime, resolved, false); err != nil {
		return nil, errors.New("maintenance state must be outside the checkout in runtime")
	}
	if _, err := os.Lstat(resolved); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	raw, err := readBoundedFile(resolved, 512*1024)
	if err != nil {
		return nil, err
	}
	var state map[string]string
	if json.Unmarshal(raw, &state) != nil || len(state) > 4096 {
		return nil, errors.New("maintenance state is invalid")
	}
	return state, nil
}

var maintenanceSecretRE = regexp.MustCompile(`(?im)(^[ \t]*(?:password|token|secret|api_key|credentials?)[ \t]*:[ \t]*\S+|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9]{20,}|(?:DEEPSEEK|OPENAI|ANTHROPIC|GITHUB|GH)_?(?:API_)?KEY\s*[:=]\s*\S+)`)

// MaintenanceIssue is a finding, never an approval or a canonical fact.
type MaintenanceIssue struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// MaintenanceInput selects complete changed documents, then their ancestors
// and scoped context. Fingerprints refer to exact bytes, including metadata.
// A full batch leaves the remaining documents unacknowledged for the next task.
func (r *Router) MaintenanceInput(previous map[string]string) (ReviewData, error) {
	docs, err := r.Docs()
	if err != nil {
		return ReviewData{}, err
	}
	candidates, err := r.CandidateFiles()
	if err != nil {
		return ReviewData{}, err
	}
	paths := make([]string, 0, len(docs)+len(candidates))
	for _, doc := range docs {
		paths = append(paths, doc.Path)
	}
	paths = append(paths, candidates...)
	sort.Strings(paths)
	data := ReviewData{Files: map[string]string{}}
	addIssue := func(path, reason string) {
		if len(data.Issues) < 30 && len(path) <= 512 {
			data.Issues = append(data.Issues, MaintenanceIssue{path, reason})
			if encoded, err := json.Marshal(data); err != nil || len(encoded) > reviewBatchLimit {
				data.Issues = data.Issues[:len(data.Issues)-1]
			}
		}
	}
	available := map[string]ReviewSource{}
	changed := []string{}
	for _, path := range paths {
		_, meta, err := r.ReadDoc(path)
		if err != nil {
			return ReviewData{}, err
		}
		if sensitive(meta) {
			data.Skipped++
			continue
		}
		full, err := r.safePath(path, false)
		if err != nil {
			return ReviewData{}, err
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			return ReviewData{}, err
		}
		meta, body := parseDocument(string(raw))
		if sensitive(meta) {
			data.Skipped++
			continue
		}
		hash := contentDigest(raw)
		inspection := strings.TrimPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\ufeff")
		if maintenanceSecretRE.MatchString(inspection) || !utf8.Valid(raw) || strings.IndexByte(string(raw), 0) >= 0 {
			data.Skipped++
			addIssue(path, "sensitive_content_excluded")
			continue
		}
		if len(raw) > reviewBatchLimit/2 {
			data.Skipped++
			addIssue(path, "document_too_large")
			continue
		}
		available[path] = ReviewSource{Source: path, Metadata: reviewMetadata(meta), Body: body, Content: string(raw)}
		probe := ReviewData{Canonical: []ReviewSource{available[path]}}
		encoded, _ := json.Marshal(probe)
		if len(encoded)+20*1024 > reviewBatchLimit {
			delete(available, path)
			data.Skipped++
			addIssue(path, "document_too_large")
			continue
		}
		if previous[path] != hash {
			changed = append(changed, path)
		}
		if strings.HasPrefix(path, "inbox/") {
			addIssue(path, "candidate_requires_owner_review")
		} else if r.IsStale(meta) {
			addIssue(path, "stale_context_requires_evidence")
		}
	}
	for _, path := range paths {
		doc, ok := available[path]
		if !ok || strings.HasPrefix(path, "inbox/") {
			continue
		}
		seen := map[string]bool{path: true}
		parent := parentOf(doc.Metadata)
		for parent != "" {
			if seen[parent] {
				addIssue(path, "hierarchy_cycle_requires_review")
				break
			}
			seen[parent] = true
			parentDoc, ok := available[parent]
			if !ok {
				addIssue(path, "parent_unavailable_requires_review")
				break
			}
			parent = parentOf(parentDoc.Metadata)
		}
		for _, match := range maintenanceLinkRE.FindAllStringSubmatch(doc.Body, -1) {
			link := strings.Split(match[2], "#")[0]
			if !strings.HasSuffix(link, ".md") || strings.Contains(link, ":") {
				continue
			}
			resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(path), link)))
			full, err := r.safePath(resolved, false)
			if err != nil || regularFile(full) != nil {
				addIssue(path, "broken_link_requires_review")
				break
			}
		}
	}
	add := func(path string) bool {
		if _, ok := data.Files[path]; ok {
			return true
		}
		if len(data.Files) >= 16 {
			return false
		}
		source, ok := available[path]
		if !ok {
			return false
		}
		if !strings.HasPrefix(path, "inbox/") && len(data.Canonical) >= maxAutoEdits {
			return false
		}
		probe := data
		probe.Files = make(map[string]string, len(data.Files)+1)
		for inputPath, hash := range data.Files {
			probe.Files[inputPath] = hash
		}
		probe.Files[path] = contentDigest([]byte(source.Content))
		if strings.HasPrefix(path, "inbox/") {
			probe.Items = append(append([]ReviewItem{}, data.Items...), ReviewItem{Source: path, Metadata: source.Metadata, Body: source.Body})
		} else {
			probe.Canonical = append(append([]ReviewSource{}, data.Canonical...), source)
		}
		encoded, err := json.Marshal(probe)
		if err != nil || len(encoded)+2048 > reviewBatchLimit {
			return false
		}
		data.Items, data.Canonical, data.Files = probe.Items, probe.Canonical, probe.Files
		return true
	}
	// Reserve room for each changed document before optional related material.
	for _, path := range changed {
		if len(data.Files) >= maxAutoEdits/2 || !add(path) {
			data.Remaining++
		}
	}
	seeds := make([]string, 0, len(data.Files))
	for path := range data.Files {
		seeds = append(seeds, path)
	}
	sort.Strings(seeds)
	for _, path := range seeds {
		seen := map[string]bool{path: true}
		parent := parentOf(available[path].Metadata)
		for parent != "" && !seen[parent] {
			seen[parent] = true
			if !add(parent) {
				addIssue(path, "related_context_over_budget")
				break
			}
			parent = parentOf(available[parent].Metadata)
		}
		if strings.HasPrefix(path, "inbox/") {
			terms := strings.Join(asList(available[path].Metadata["scope"]), " ")
			if terms != "" {
				selected, err := r.Select(terms, false, false, "")
				if err != nil {
					return ReviewData{}, err
				}
				for _, related := range selected {
					add(related.Path)
				}
			}
		}
	}
	// Recount after ancestors/scoped documents filled some of the backlog.
	data.Remaining = 0
	for _, path := range changed {
		if _, ok := data.Files[path]; !ok {
			data.Remaining++
		}
	}
	return data, nil
}

// SnapshotMaintenanceInput reconstructs the exact model-visible canonical
// input at the transaction boundary. An unseen or changed file is not editable.
func (r *Router) SnapshotMaintenanceInput(files map[string]string) (ReviewData, error) {
	data := ReviewData{Files: files}
	if len(files) > 128 {
		return data, errors.New("maintenance content snapshot is too large")
	}
	for path, hash := range files {
		full, err := r.safePath(path, false)
		if err != nil {
			return data, errors.New("maintenance content snapshot is invalid")
		}
		if err := regularFile(full); err != nil {
			return data, err
		}
		raw, err := os.ReadFile(full)
		if err != nil || contentDigest(raw) != hash {
			return data, errors.New("maintenance content snapshot changed")
		}
		meta, body := parseDocument(string(raw))
		if sensitive(meta) {
			return data, errors.New("maintenance content snapshot is sensitive")
		}
		if !strings.HasPrefix(path, "inbox/") {
			data.Canonical = append(data.Canonical, ReviewSource{Source: path, Metadata: meta, Body: body, Content: string(raw)})
		}
	}
	return data, nil
}

var maintenanceLinkRE = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
var maintenanceListRE = regexp.MustCompile(`^(?:[-*+] |[0-9]+[.)] )`)

func splitMaintenanceContent(text string) (string, string) {
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 1 && strings.TrimSuffix(strings.TrimPrefix(lines[0], "\ufeff"), "\n") != "" && strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff")) == "---" {
		offset := len(lines[0])
		for _, line := range lines[1:] {
			offset += len(line)
			if strings.TrimSpace(line) == "---" {
				return text[:offset], text[offset:]
			}
		}
	}
	return "", text
}

func maintenanceLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "#")
	line = strings.TrimSpace(line)
	line = maintenanceListRE.ReplaceAllString(line, "")
	return strings.TrimSpace(line)
}

// ValidateMaintenanceEdit deliberately accepts only lossless structural edits.
// Semantic rewrites and migrations go through owner review. This is a code
// gate, not a request for the model to judge its own factual correctness.
func (r *Router) ValidateMaintenanceEdit(data ReviewData, source, target, claimType, content string) error {
	known := map[string]ReviewSource{}
	evidence := map[string]bool{}
	labels := map[string]bool{}
	for _, doc := range data.Canonical {
		known[doc.Source] = doc
		_, body := splitMaintenanceContent(doc.Content)
		for _, line := range strings.Split(body, "\n") {
			atom := maintenanceLine(line)
			labels[atom] = true
			// Cross-document summaries must retain the child's claim category.
			// An unspecified category is not evidence for a canonical claim.
			if doc.Source == target || (parentOf(doc.Metadata) == target && metaString(doc.Metadata, "claim_type") != "" && metaString(doc.Metadata, "claim_type") == claimType) {
				evidence[atom] = true
			}
			for _, match := range maintenanceLinkRE.FindAllStringSubmatch(line, -1) {
				labels[match[1]] = true
			}
		}
	}
	old, ok := known[target]
	if !ok || old.Content == "" {
		return errors.New("maintenance target was not supplied in full")
	}
	if _, ok := known[source]; !ok {
		return errors.New("maintenance content cannot promote a candidate")
	}
	if declared := metaString(old.Metadata, "claim_type"); declared != "" && declared != claimType {
		return errors.New("maintenance content changed claim type")
	}
	oldMeta, oldBody := splitMaintenanceContent(old.Content)
	newMeta, newBody := splitMaintenanceContent(content)
	if oldMeta != newMeta {
		return errors.New("maintenance content changed protected metadata")
	}
	newLines := map[string]bool{}
	oldLinks := map[string]bool{}
	for _, match := range maintenanceLinkRE.FindAllString(oldBody, -1) {
		oldLinks[match] = true
	}
	for _, line := range strings.Split(newBody, "\n") {
		atom := maintenanceLine(line)
		newLines[atom] = true
		if evidence[atom] || atom == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			switch atom {
			case "Summary", "Details", "Related", "摘要", "详情", "相关文档":
				continue
			}
		}
		// New navigation is restricted to a complete local document link.
		match := maintenanceLinkRE.FindStringSubmatch(atom)
		if len(match) != 3 || match[0] != atom {
			return errors.New("maintenance content added unsupported prose")
		}
		path := strings.Split(match[2], "#")[0]
		if !strings.HasSuffix(path, ".md") || filepath.IsAbs(path) || strings.Contains(path, ":") || strings.Contains(path, "\\") {
			return errors.New("maintenance content link is invalid")
		}
		resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(target), path)))
		if _, ok := known[resolved]; !ok {
			return errors.New("maintenance content link is outside supplied context")
		}
		// The label must already exist as text in the evidence.
		if !labels[maintenanceLine(match[1])] {
			return errors.New("maintenance content link label lacks evidence")
		}
	}
	for _, line := range strings.Split(oldBody, "\n") {
		atom := maintenanceLine(line)
		if atom != "" && !newLines[atom] {
			// A link repair may change only its destination, not its label.
			oldMatch := maintenanceLinkRE.FindStringSubmatch(atom)
			found := false
			if len(oldMatch) == 3 && oldMatch[0] == atom {
				for candidate := range newLines {
					m := maintenanceLinkRE.FindStringSubmatch(candidate)
					if len(m) == 3 && m[0] == candidate && m[1] == oldMatch[1] {
						found = true
						break
					}
				}
			}
			if !found {
				return errors.New("maintenance content removed distinct existing text")
			}
		}
	}
	// Existing broken links are findings; newly introduced broken links fail.
	for _, match := range maintenanceLinkRE.FindAllStringSubmatch(newBody, -1) {
		if oldLinks[match[0]] {
			continue
		}
		path := strings.Split(match[2], "#")[0]
		if !strings.HasSuffix(path, ".md") || strings.Contains(path, ":") || filepath.IsAbs(path) {
			return errors.New("maintenance content link is invalid")
		}
		resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(target), path)))
		if _, ok := known[resolved]; !ok {
			return errors.New("maintenance content link is outside supplied context")
		}
	}
	return nil
}

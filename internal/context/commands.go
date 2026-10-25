package contextrepo

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var candidateScopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type commandOptions struct {
	bools  map[string]bool
	values map[string]string
	args   []string
}

func parseOptions(args []string, boolFlags, valueFlags map[string]bool) (commandOptions, error) {
	result := commandOptions{bools: make(map[string]bool), values: make(map[string]string)}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			result.args = append(result.args, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			result.args = append(result.args, arg)
			continue
		}
		nameValue := strings.TrimPrefix(arg, "--")
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := strings.Cut(nameValue, "=")
			if boolFlags[name] {
				if hasValue {
					return commandOptions{}, fmt.Errorf("flag --%s does not take a value", name)
				}
				result.bools[name] = true
				continue
			}
			if valueFlags[name] {
				if !hasValue {
					if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
						return commandOptions{}, fmt.Errorf("flag --%s requires a value", name)
					}
					i++
					value = args[i]
				}
				result.values[name] = value
				continue
			}
			return commandOptions{}, fmt.Errorf("unsupported flag --%s", name)
		}
		return commandOptions{}, fmt.Errorf("unsupported argument %s", arg)
	}
	return result, nil
}

// Run is the stable CLI entrypoint used by cmd/folio/main.go. It accepts
// interspersed options, reports unsupported commands explicitly, and keeps
// command errors on the supplied error writer.
func Run(args []string, out, errOut io.Writer) int {
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	if len(args) == 0 {
		fmt.Fprintln(errOut, "context: command is required")
		return 2
	}
	router, err := NewFromEnv()
	if err != nil {
		fmt.Fprintf(errOut, "context: %v\n", err)
		return 2
	}
	command := args[0]
	commandArgs := args[1:]
	var runErr error
	switch command {
	case "status":
		runErr = runStatus(router, commandArgs, out)
	case "current":
		runErr = runCurrent(router, commandArgs, out)
	case "build":
		runErr = runBuild(router, commandArgs, out)
	case "search":
		runErr = runSearch(router, commandArgs, out)
	case "stale":
		runErr = runStale(router, commandArgs, out)
	case "review":
		runErr = runReview(router, commandArgs, out)
	case "candidate":
		runErr = runCandidate(router, commandArgs, out)
	case "maintain":
		runErr = runMaintain(router, commandArgs, out)
	case "hierarchy":
		runErr = runHierarchy(router, commandArgs, out)
	case "links":
		runErr = runLinks(router, commandArgs, out)
	case "agent":
		runErr = runAgent(router, commandArgs, out)
	default:
		runErr = fmt.Errorf("unsupported command %q", command)
	}
	if runErr != nil {
		var exitOne commandExitOne
		if errors.As(runErr, &exitOne) {
			return 1
		}
		fmt.Fprintf(errOut, "context: %v\n", runErr)
		return 2
	}
	return 0
}

func noArgs(args []string) error {
	if len(args) != 0 {
		return errors.New("unexpected arguments")
	}
	return nil
}

func runStatus(r *Router, args []string, out io.Writer) error {
	if err := noArgs(args); err != nil {
		return err
	}
	docs, err := r.Docs()
	if err != nil {
		return err
	}
	layers := make(map[string]int)
	for _, doc := range docs {
		key := valueOr(doc.Metadata, "layer", "unclassified")
		layers[key]++
	}
	keys := make([]string, 0, len(layers))
	for key := range layers {
		keys = append(keys, key)
	}
	// Go's map iteration is intentionally random; status is a deterministic
	// inspection command so its layer summary is sorted.
	sortStrings(keys)
	candidates, err := r.CandidateFiles()
	if err != nil {
		return err
	}
	counts, err := r.accessCounts()
	if err != nil {
		return err
	}
	staleCount := 0
	for _, doc := range docs {
		if layer(doc.Metadata) == "L1" && r.IsStale(doc.Metadata) {
			staleCount++
		}
	}
	fmt.Fprintf(out, "repository: %s\ncanonical documents: %d\n", r.Root, len(docs))
	if len(keys) == 0 {
		fmt.Fprintln(out, "layers: none")
	} else {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", key, layers[key]))
		}
		fmt.Fprintf(out, "layers: %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(out, "pending inbox files: %d\nstale active documents: %d\nrecorded access events: %d\n", len(candidates), staleCount, countAccesses(counts))
	return nil
}

func countAccesses(counts map[string]accessCount) int {
	total := 0
	for _, item := range counts {
		total += item.Count
	}
	return total
}

func runCurrent(r *Router, args []string, out io.Writer) error {
	if err := noArgs(args); err != nil {
		return err
	}
	docs, err := r.Docs()
	if err != nil {
		return err
	}
	for _, doc := range docs {
		if layer(doc.Metadata) != "L1" {
			continue
		}
		first := ""
		if lines := strings.Split(doc.Body, "\n"); len(lines) > 0 {
			first = lines[0]
		}
		fmt.Fprintf(out, "%s | updated=%s | review_after=%s | stale=%t\n%s\n", doc.Path, valueOr(doc.Metadata, "updated", "?"), valueOr(doc.Metadata, "review_after", "?"), r.IsStale(doc.Metadata), first)
	}
	return nil
}

func runBuild(r *Router, args []string, out io.Writer) error {
	options, err := parseOptions(args, map[string]bool{"include-history": true, "include-sensitive": true, "no-record": true, "stdout": true}, map[string]bool{"device": true})
	if err != nil {
		return err
	}
	if len(options.args) == 0 {
		return errors.New("build requires a task")
	}
	task := strings.Join(options.args, " ")
	includeHistory := options.bools["include-history"]
	includeSensitive := options.bools["include-sensitive"]
	selected, err := r.Select(task, includeHistory, includeSensitive, options.values["device"])
	if err != nil {
		return err
	}
	if !options.bools["no-record"] {
		if err := r.RecordAccess(task, selected); err != nil {
			return err
		}
	}
	content := r.Packet(task, selected, includeHistory, includeSensitive, options.values["device"])
	if options.bools["stdout"] {
		_, err = io.WriteString(out, content)
		return err
	}
	slug := slugify(task)
	stamp := r.now().Format("20060102T150405Z")
	path := filepath.Join(r.Root, "generated", "context-packets", stamp+"-"+slug+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	rel, _ := r.relative(path)
	fmt.Fprintf(out, "packet: %s\nselected sources:\n", rel)
	for _, item := range selected {
		fmt.Fprintf(out, "- %s (%s)\n", item.Path, item.Reason)
	}
	return nil
}

func slugify(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		result = "task"
	}
	if len(result) > 60 {
		result = result[:60]
	}
	return result
}

func runSearch(r *Router, args []string, out io.Writer) error {
	options, err := parseOptions(args, map[string]bool{"include-sensitive": true}, nil)
	if err != nil {
		return err
	}
	if len(options.args) == 0 {
		return errors.New("search requires a query")
	}
	needle := strings.ToLower(strings.Join(options.args, " "))
	docs, err := r.Docs()
	if err != nil {
		return err
	}
	for _, doc := range docs {
		if sensitive(doc.Metadata) && !options.bools["include-sensitive"] {
			continue
		}
		haystack := strings.ToLower(doc.Path + "\n" + doc.Body)
		if !strings.Contains(haystack, needle) {
			continue
		}
		fmt.Fprintf(out, "%s | layer=%s\n", doc.Path, valueOr(doc.Metadata, "layer", "?"))
		for _, line := range strings.Split(doc.Body, "\n") {
			if strings.Contains(strings.ToLower(line), needle) {
				fmt.Fprintf(out, "  %s\n", strings.TrimSpace(line))
			}
		}
	}
	return nil
}

func runStale(r *Router, args []string, out io.Writer) error {
	if err := noArgs(args); err != nil {
		return err
	}
	docs, err := r.Docs()
	if err != nil {
		return err
	}
	found := false
	for _, doc := range docs {
		if !r.IsStale(doc.Metadata) {
			continue
		}
		found = true
		fmt.Fprintf(out, "%s | layer=%s | review_after=%s\n", doc.Path, valueOr(doc.Metadata, "layer", "?"), valueOr(doc.Metadata, "review_after", "?"))
	}
	if !found {
		fmt.Fprintln(out, "no stale documents")
	}
	return nil
}

func runReview(r *Router, args []string, out io.Writer) error {
	options, err := parseOptions(args, map[string]bool{"proposals": true}, map[string]bool{"runtime-dir": true})
	if err != nil {
		return err
	}
	if len(options.args) != 0 {
		return errors.New("unexpected arguments")
	}
	candidates, err := r.CandidateFiles()
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		fmt.Fprintln(out, "no pending candidate files")
	} else {
		for _, path := range candidates {
			fmt.Fprintln(out, path)
		}
	}
	if options.bools["proposals"] {
		return r.ShowProposals(options.values["runtime-dir"], out)
	}
	return nil
}

func runCandidate(r *Router, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "add" {
		return errors.New("candidate requires the add subcommand")
	}
	options, err := parseOptions(args[1:], nil, map[string]bool{
		"title": true, "source": true, "claim-type": true, "scope": true,
		"observation": true, "proposal": true, "confirmation-needed": true,
		"evidence": true, "confidence": true, "sensitivity": true,
		"observed": true, "device": true,
	})
	if err != nil {
		return err
	}
	if len(options.args) != 0 {
		return errors.New("candidate add does not accept positional arguments")
	}
	title, err := candidateText("title", options.values["title"], 160, false)
	if err != nil {
		return err
	}
	source, err := candidateText("source", options.values["source"], 1000, false)
	if err != nil {
		return err
	}
	observation, err := candidateText("observation", options.values["observation"], 12000, true)
	if err != nil {
		return err
	}
	proposal, err := candidateText("proposal", options.values["proposal"], 12000, true)
	if err != nil {
		return err
	}
	confirmation, err := candidateText("confirmation-needed", options.values["confirmation-needed"], 4000, true)
	if err != nil {
		return err
	}
	evidence := options.values["evidence"]
	if evidence == "" {
		evidence = "No additional evidence supplied."
	}
	if evidence, err = candidateText("evidence", evidence, 8000, true); err != nil {
		return err
	}
	claimType, err := candidateText("claim-type", options.values["claim-type"], 32, false)
	if err != nil {
		return err
	}
	claimType = strings.ToLower(claimType)
	if !claimTypes[claimType] {
		return fmt.Errorf("claim-type must be one of: %s", strings.Join(sortedClaimTypes(), ", "))
	}
	confidence := options.values["confidence"]
	if confidence == "" {
		confidence = "medium"
	} else if confidence, err = candidateText("confidence", confidence, 16, false); err != nil {
		return err
	}
	confidence = strings.ToLower(confidence)
	if confidence != "low" && confidence != "medium" && confidence != "high" {
		return errors.New("confidence must be low, medium, or high")
	}
	sensitivity := options.values["sensitivity"]
	if sensitivity == "" {
		sensitivity = "private"
	} else if sensitivity, err = candidateText("sensitivity", sensitivity, 16, false); err != nil {
		return err
	}
	sensitivity = strings.ToLower(sensitivity)
	if sensitivity != "public" && sensitivity != "private" && sensitivity != "sensitive" {
		return errors.New("sensitivity must be public, private, or sensitive")
	}
	observed := options.values["observed"]
	if observed == "" {
		observed = r.now().Format(dateFormat)
	}
	if _, err := time.Parse(dateFormat, observed); err != nil {
		return errors.New("observed must use YYYY-MM-DD")
	}
	scopeText, err := candidateText("scope", options.values["scope"], 1400, false)
	if err != nil {
		return err
	}
	scopes := make([]string, 0, 8)
	seen := make(map[string]bool)
	for _, item := range strings.Split(scopeText, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if !candidateScopePattern.MatchString(item) {
			return errors.New("scope must be comma-separated lowercase topic names")
		}
		if !seen[item] {
			seen[item] = true
			scopes = append(scopes, item)
		}
	}
	if len(scopes) == 0 || len(scopes) > 20 {
		return errors.New("scope must contain between one and twenty topics")
	}
	device := options.values["device"]
	if device != "" {
		if device, err = candidateText("device", device, 80, false); err != nil {
			return err
		}
	}
	inbox := filepath.Join(r.Root, "inbox")
	if info, statErr := os.Lstat(inbox); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("inbox is not a regular directory")
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if err := os.MkdirAll(inbox, 0o700); err != nil {
			return err
		}
	} else {
		return statErr
	}
	base := "observation-" + r.now().Format("20060102T150405Z") + "-" + slugify(title)
	content := "---\n" +
		"type: candidate\nstatus: pending-review\ncandidate_kind: observation\n" +
		"observed: " + observed + "\nsource: " + source + "\nclaim_type: " + claimType +
		"\nconfidence: " + confidence + "\nscope: [" + strings.Join(scopes, ", ") +
		"]\nsensitivity: " + sensitivity + "\n"
	if device != "" {
		content += "device: " + device + "\n"
	}
	content += "---\n\n# Candidate: " + title + "\n\n## Observation\n\n" + observation +
		"\n\n## Proposed claim\n\n" + proposal + "\n\n## Evidence and conflicts\n\n" + evidence +
		"\n\n## Confirmation needed\n\n" + confirmation +
		"\n\n## Review\n\nDecision: pending\nReviewer:\nReviewed:\nReason:\n"
	for suffix := 0; suffix < 1000; suffix++ {
		name := base + ".md"
		if suffix > 0 {
			name = fmt.Sprintf("%s-%d.md", base, suffix)
		}
		path := filepath.Join(inbox, name)
		handle, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(openErr, os.ErrExist) {
			continue
		}
		if openErr != nil {
			return openErr
		}
		if _, writeErr := handle.WriteString(content); writeErr != nil {
			handle.Close()
			return writeErr
		}
		if closeErr := handle.Close(); closeErr != nil {
			return closeErr
		}
		rel, _ := r.relative(path)
		fmt.Fprintf(out, "candidate: %s\nstatus: pending-review; canonical files unchanged\n", rel)
		return nil
	}
	return errors.New("could not allocate a candidate filename")
}

func candidateText(field, value string, maximum int, multiline bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("%s contains NUL", field)
	}
	if !multiline && (strings.ContainsAny(value, "\r\n")) {
		return "", fmt.Errorf("%s must be one line", field)
	}
	for _, r := range value {
		if r < 32 && r != '\n' && r != '\t' {
			return "", fmt.Errorf("%s contains unsupported control characters", field)
		}
	}
	if len([]rune(value)) > maximum {
		return "", fmt.Errorf("%s is too long", field)
	}
	return value, nil
}

func sortedClaimTypes() []string {
	result := make([]string, 0, len(claimTypes))
	for value := range claimTypes {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func runMaintain(r *Router, args []string, out io.Writer) error {
	options, err := parseOptions(args, map[string]bool{"check": true, "read-only": true, "stdout": true}, map[string]bool{"min-accesses": true, "device": true, "report-dir": true})
	if err != nil {
		return err
	}
	if len(options.args) != 0 {
		return errors.New("unexpected arguments")
	}
	minAccesses := 3
	if raw := options.values["min-accesses"]; raw != "" {
		minAccesses, err = strconv.Atoi(raw)
		if err != nil || minAccesses < 0 {
			return errors.New("min-accesses must be a non-negative integer")
		}
	}
	pathOrReport, err := r.WriteReport(options.values["device"], minAccesses, options.values["report-dir"])
	if err != nil {
		return err
	}
	report := pathOrReport
	if options.values["report-dir"] != "" {
		data, readErr := os.ReadFile(pathOrReport)
		if readErr != nil {
			return readErr
		}
		report = string(data)
		if options.bools["stdout"] {
			_, err = io.WriteString(out, report)
		} else {
			fmt.Fprintf(out, "report: %s\n", pathOrReport)
		}
	} else {
		_, err = io.WriteString(out, report)
	}
	if err != nil {
		return err
	}
	if options.bools["check"] && strings.Contains(report, "Summary: pending=0; stale_l1=0; residual_links=0; git_dirty=no") {
		return nil
	}
	if options.bools["check"] {
		return commandExitOne{}
	}
	return nil
}

type commandExitOne struct{}

func (commandExitOne) Error() string { return "maintenance action is needed" }

func runHierarchy(r *Router, args []string, out io.Writer) error {
	if err := noArgs(args); err != nil {
		return err
	}
	docs, err := r.Docs()
	if err != nil {
		return err
	}
	sort.Slice(docs, func(i, j int) bool {
		if depth(docs[i].Metadata) != depth(docs[j].Metadata) {
			return depth(docs[i].Metadata) < depth(docs[j].Metadata)
		}
		return docs[i].Path < docs[j].Path
	})
	for _, doc := range docs {
		parent := parentOf(doc.Metadata)
		if parent == "" {
			parent = "-"
		}
		fmt.Fprintf(out, "D%d %s | parent=%s | layer=%s\n", depth(doc.Metadata), doc.Path, parent, valueOr(doc.Metadata, "layer", "?"))
	}
	return nil
}

func runLinks(r *Router, args []string, out io.Writer) error {
	options, err := parseOptions(args, map[string]bool{"propose": true}, map[string]bool{"min-accesses": true, "apply": true})
	if err != nil {
		return err
	}
	if len(options.args) != 0 {
		return errors.New("unexpected arguments")
	}
	if candidate := options.values["apply"]; candidate != "" {
		if err := r.ApplyResidualCandidate(candidate); err != nil {
			return err
		}
		fmt.Fprintf(out, "applied residual link: %s\n", candidate)
		return nil
	}
	minAccesses := 3
	if raw := options.values["min-accesses"]; raw != "" {
		minAccesses, err = strconv.Atoi(raw)
		if err != nil || minAccesses < 0 {
			return errors.New("min-accesses must be a non-negative integer")
		}
	}
	recommendations, err := r.ResidualRecommendations(minAccesses)
	if err != nil {
		return err
	}
	if len(recommendations) == 0 {
		fmt.Fprintln(out, "no residual-link recommendations")
		return nil
	}
	for _, item := range recommendations {
		fmt.Fprintf(out, "%s -> %s | full accesses=%d | last=%s\n", item.Child, item.Parent, item.FullAccesses, item.Last)
		if !options.bools["propose"] {
			continue
		}
		name := "residual-link-" + slugify(item.Child) + ".md"
		path := filepath.Join(r.Root, "inbox", name)
		if _, statErr := os.Lstat(path); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		content := fmt.Sprintf("---\ntype: candidate\nstatus: pending-review\ncandidate_kind: residual-link\nobserved: %s\nsource: generated/usage/access-log.jsonl\nclaim_type: inference\nconfidence: medium\nparent: %s\nchild: %s\naccess_count: %d\nscope: [%s]\nsensitivity: private\n---\n\n# Candidate residual link: %s\n\n## Observation\n\nThe detailed node was selected in full %d times; the latest recorded access was %s.\n\n## Proposed change\n\nAfter review, add a compact link from `%s` to `%s`. Keep the parent broad and leave detailed claims in the child.\n\n## Confirmation needed\n\nConfirm that repeated access represents durable relevance rather than a temporary project or debugging burst.\n\n## Review\n\nDecision: pending\nReviewer:\nReviewed:\nReason:\n", time.Now().UTC().Format(dateFormat), item.Parent, item.Child, item.FullAccesses, item.Child, item.Child, item.FullAccesses, item.Last, item.Parent, item.Child)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
		rel, _ := r.relative(path)
		fmt.Fprintf(out, "  candidate: %s\n", rel)
	}
	return nil
}

func runAgent(r *Router, args []string, out io.Writer) error {
	options, err := parseOptions(args, map[string]bool{"dry-run": true, "force": true, "auto": true}, map[string]bool{"maintenance-state": true, "auto-report": true, "device": true, "runtime-dir": true, "model": true, "provider": true, "runner": true, "timeout": true, "report-prompt": true, "report-out": true})
	if err != nil {
		return err
	}
	if len(options.args) != 0 {
		return errors.New("unexpected arguments")
	}
	runner := options.values["runner"]
	if runner == "" {
		runner = "codex"
	}
	agent := AgentOptions{DryRun: options.bools["dry-run"], Force: options.bools["force"], Device: "NAS", RuntimeDir: options.values["runtime-dir"], Model: options.values["model"], Provider: options.values["provider"], Runner: runner, Auto: options.bools["auto"], AutoReport: options.values["auto-report"], MaintenanceState: options.values["maintenance-state"], ReportPrompt: options.values["report-prompt"], ReportOutput: options.values["report-out"], Timeout: 15 * time.Minute}
	if raw := options.values["device"]; raw != "" {
		agent.Device = raw
	}
	if raw := options.values["timeout"]; raw != "" {
		seconds, parseErr := strconv.Atoi(raw)
		if parseErr != nil || seconds <= 0 {
			return errors.New("timeout must be a positive number of seconds")
		}
		agent.Timeout = time.Duration(seconds) * time.Second
	}
	return r.RunAgent(agent, out, io.Discard)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

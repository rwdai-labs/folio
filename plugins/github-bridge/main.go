package main

import (
	"context"
	"flag"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxEventsPerCycle = 10
	version           = "1.0.0"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	configPath := flag.String("config", "bridge.json", "path to bridge.json")
	once := flag.Bool("once", false, "run one cycle and exit")
	dryRun := flag.Bool("dry-run", false, "print events instead of posting them")
	check := flag.Bool("check", false, "validate configuration and credentials, then exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	value, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *check {
		repos := 0
		for _, source := range value.Sources {
			repos += len(source.Repos)
		}
		fmt.Printf("config ok: %d source(s), %d repository(ies), hook %s at %s\n",
			len(value.Sources), repos, value.Folio.HookID, strings.TrimRight(value.Folio.URL, "/"))
		return
	}
	state, err := loadState(value.StateFile)
	if err != nil {
		log.Fatalf("state: %v", err)
	}
	hook, err := newHookClient(value)
	if err != nil {
		log.Fatalf("hook: %v", err)
	}
	bridge := &bridge{config: value, state: state, hook: hook, dryRun: *dryRun, log: log.Default()}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *once {
		bridge.syncOnce(ctx)
		return
	}
	interval := time.Duration(value.IntervalSeconds) * time.Second
	bridge.log.Printf("bridge %s watching %d source(s) every %s", version, len(value.Sources), interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		bridge.syncOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type bridge struct {
	config config
	state  *bridgeState
	hook   *hookClient
	dryRun bool
	log    *log.Logger
}

// syncOnce is deliberately best-effort per repository: one failing repository
// must not stop the others, and an event is marked seen only after Folio
// accepted it.
func (b *bridge) syncOnce(ctx context.Context) {
	for _, source := range b.config.Sources {
		token, err := readToken(source.TokenFile)
		if err != nil {
			b.log.Printf("source %s: %v", source.Name, err)
			continue
		}
		client := newGitHubClient(token)
		for _, repo := range source.Repos {
			b.syncRepo(ctx, source, client, repo)
		}
	}
	if err := b.state.save(b.config.StateFile); err != nil {
		b.log.Printf("state: %v", err)
	}
}

func (b *bridge) syncRepo(ctx context.Context, source sourceConfig, client *githubClient, repo string) {
	state, ok := b.state.Repos[repo]
	if !ok {
		state = &repoState{Seen: map[string]string{}}
		b.state.Repos[repo] = state
	}
	if holdActive(state, time.Now()) {
		b.log.Printf("%s: rate limited until %s", repo, state.HoldUntil)
		return
	}
	issues, etag, notModified, hold, err := client.listIssues(ctx, repo, state.ETag)
	if err != nil {
		if !hold.IsZero() {
			state.HoldUntil = hold.UTC().Format(time.RFC3339)
			b.log.Printf("%s: %v, holding until %s", repo, err, state.HoldUntil)
			return
		}
		b.log.Printf("%s: %v", repo, err)
		return
	}
	if notModified {
		return
	}
	if etag != "" {
		state.ETag = etag
	}
	if state.Seen == nil {
		state.Seen = map[string]string{}
	}
	if !state.Baseline {
		for _, issue := range issues {
			state.Seen[strconv.Itoa(issue.Number)] = issue.UpdatedAt
		}
		state.Baseline = true
		b.log.Printf("%s: baseline recorded with %d issue(s); changes from now on will be sent", repo, len(issues))
		return
	}
	sent := 0
	for _, issue := range issues {
		key := strconv.Itoa(issue.Number)
		previous, seen := state.Seen[key]
		if seen && previous == issue.UpdatedAt {
			continue
		}
		if issue.isPull() && !source.IncludePRs {
			state.Seen[key] = issue.UpdatedAt
			continue
		}
		if !matchesLabels(issue, source.Labels) {
			state.Seen[key] = issue.UpdatedAt
			continue
		}
		if sent >= maxEventsPerCycle {
			// Left unseen on purpose: the next cycle picks it up.
			break
		}
		eventID := issueEventID(repo, issue)
		payload := issuePayload(repo, issue, !seen)
		if b.dryRun {
			b.log.Printf("dry-run %s %s", eventID, payload["title"])
			state.Seen[key] = issue.UpdatedAt
			sent++
			continue
		}
		if err := b.hook.send(ctx, eventID, payload); err != nil {
			b.log.Printf("%s: event %s failed: %v", repo, eventID, err)
			if isPermanent(err) {
				state.Seen[key] = issue.UpdatedAt
			}
			continue
		}
		state.Seen[key] = issue.UpdatedAt
		sent++
		b.log.Printf("%s: queued %s", repo, payload["title"])
	}
}

func matchesLabels(issue githubIssue, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, label := range issue.Labels {
		for _, filter := range wanted {
			if strings.EqualFold(label.Name, filter) {
				return true
			}
		}
	}
	return false
}

func issuePayload(repo string, issue githubIssue, opened bool) map[string]any {
	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, label.Name)
	}
	event := "updated"
	if opened {
		event = "opened"
	}
	title := truncateRunes(issue.Title, 80)
	return map[string]any{
		"title":  fmt.Sprintf("issue #%d %s", issue.Number, title),
		"source": "github",
		"repo":   repo,
		"issue":  issue.Number,
		"event":  event,
		"state":  issue.State,
		"url":    issue.HTMLURL,
		"labels": strings.Join(labels, ","),
	}
}

// issueEventID is stable for one version of an issue and short enough for the
// hook contract's 128-character limit.
func issueEventID(repo string, issue githubIssue) string {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(repo))
	stamp := issue.UpdatedAt
	if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
		stamp = strconv.FormatInt(parsed.Unix(), 10)
	} else {
		stamp = strconv.FormatUint(uint64(hashString(stamp)), 10)
	}
	return fmt.Sprintf("gh-%08x-%d-%s", hasher.Sum32(), issue.Number, stamp)
}

func hashString(value string) uint32 {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(value))
	return hasher.Sum32()
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}

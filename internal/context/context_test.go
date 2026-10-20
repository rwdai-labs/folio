package contextrepo

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRouter(t *testing.T) *Router {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"core", "active", "domains", "projects", "decisions", "knowledge", "history", "inbox"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	router, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	router.Now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	return router
}

func writeDoc(t *testing.T, root, path, metadata, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("---\n"+metadata+"\n---\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSelectPreservesLayerPrivacyAndParentCycleGuard(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "domains/child.md", "layer: L1\nparent: domains/parent.md\nscope: [vllm]", "child")
	writeDoc(t, router.Root, "domains/parent.md", "layer: L2\nsensitivity: sensitive\nparent: domains/parent.md", "parent")
	selected, err := router.Select("vllm", false, false, "GPU server")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Path != "domains/child.md" {
		t.Fatalf("unexpected default selection: %#v", selected)
	}
	selected, err = router.Select("vllm", true, true, "family education")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[1].Reason != "ancestor summary" {
		t.Fatalf("unexpected explicit history selection: %#v", selected)
	}
}

func TestSymlinkAndExternalRootBoundaries(t *testing.T) {
	router := testRouter(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(router.Root, "core", "linked.md")
	if err := os.Symlink(outside, linked); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if docs, err := router.Docs(); err != nil {
		t.Fatal(err)
	} else if len(docs) != 0 {
		t.Fatalf("symlink was disclosed: %#v", docs)
	}
	if _, _, err := router.ReadDoc(linked); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestRuntimePathRejectsNewDirectoryBelowCheckoutSymlink(t *testing.T) {
	router := testRouter(t)
	linkParent := filepath.Join(t.TempDir(), "checkout-link")
	if err := os.Symlink(router.Root, linkParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := router.RuntimePath(filepath.Join(linkParent, "new-runtime")); err == nil {
		t.Fatal("runtime below a checkout symlink was accepted")
	}
}

func TestPacketStaleWarningAndExternalTelemetry(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "active/stale.md", "layer: L1\nscope: [test]\nreview_after: 2026-10-14", "stale body")
	selected, err := router.Select("test", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(router.Packet("test", selected, false, false, "NAS"), "WARNING") {
		t.Fatal("packet omitted stale warning")
	}
	runtime := t.TempDir()
	t.Setenv("PERSONAL_CONTEXT_RUNTIME", runtime)
	if err := router.RecordAccess("test secret text", selected); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(router.Root, "generated", "usage", "access-log.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("telemetry wrote into checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtime, "generated", "usage", "access-log.jsonl")); err != nil {
		t.Fatalf("external telemetry missing: %v", err)
	}
}

func TestReviewInputExcludesSensitiveAndBoundsCandidates(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "inbox/normal.md", "type: candidate\nclaim_type: inference\nscope: [test]\nprivate_token: DO-NOT-INCLUDE", "Maybe X")
	writeDoc(t, router.Root, "inbox/secret.md", "type: candidate\nsensitivity: sensitive", "DO NOT INCLUDE")
	data, err := router.ReviewInput()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Items) != 1 || data.Skipped != 1 {
		t.Fatalf("unexpected review input: %#v", data)
	}
	encoded, _ := json.Marshal(data)
	if strings.Contains(string(encoded), "DO NOT INCLUDE") || strings.Contains(string(encoded), "DO-NOT-INCLUDE") || data.Items[0].Metadata["claim_type"] != "inference" {
		t.Fatalf("privacy or claim type regression: %s", encoded)
	}
}

func TestCandidateAddCreatesPendingInboxObservation(t *testing.T) {
	router := testRouter(t)
	var output bytes.Buffer
	args := []string{
		"add", "--title", "Possible preference",
		"--source", "owner statement in current session",
		"--claim-type", "inference", "--scope", "communication,context",
		"--observation", "The owner repeatedly requested concise reports.",
		"--proposal", "The owner may prefer concise reports.",
		"--confirmation-needed", "Confirm this as a durable preference.",
		"--evidence", "Three requests in the same session.",
		"--device", "WSL",
	}
	if err := runCandidate(router, args, &output); err != nil {
		t.Fatal(err)
	}
	candidates, err := router.CandidateFiles()
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates: %v %v", candidates, err)
	}
	doc, meta, err := router.ReadDoc(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if metaString(meta, "status") != "pending-review" || metaString(meta, "candidate_kind") != "observation" || metaString(meta, "claim_type") != "inference" {
		t.Fatalf("candidate metadata: %#v", meta)
	}
	if !strings.Contains(doc.Body, "The owner may prefer concise reports.") || !strings.Contains(output.String(), "canonical files unchanged") {
		t.Fatalf("candidate output/body: %s / %s", output.String(), doc.Body)
	}
}

func TestWorkerCodexArgsUseIsolatedReviewInvocation(t *testing.T) {
	args := workerCodexArgs("/tmp/review", "/tmp/review/schema.json", "/tmp/review/proposal.json", []string{"-c", `model_provider="custom"`}, "model-x")
	joined := strings.Join(args, " ")
	for _, required := range []string{"--ignore-user-config", "--ephemeral", "--sandbox read-only", "--ignore-rules", "--disable shell_tool", "--disable unified_exec", "--disable hooks", "--disable apps", "--disable memories", "--disable multi_agent", "--disable multi_agent_v2", "--output-schema", "-o"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("worker args omitted %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"--profile", "--add-dir", "--enable", "--mcp", "--plugin", "features.multi_agent_v2=false"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("worker args contain forbidden %q: %s", forbidden, joined)
		}
	}
	if strings.Count(joined, `model_provider="custom"`) != 1 || !strings.HasSuffix(joined, " -") {
		t.Fatalf("worker args were duplicated or missing stdin marker: %s", joined)
	}
}

func TestWorkerProducesProposalOnlyReportAndSkipsUnchangedInput(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "inbox/candidate.md", "type: candidate\nclaim_type: inference", "Maybe X")
	runtime := t.TempDir()
	bin := t.TempDir()
	fake := filepath.Join(bin, "codex")
	script := "#!/bin/sh\nout=''\nprev=''\nfor arg in \"$@\"; do if [ \"$prev\" = \"-o\" ]; then out=\"$arg\"; fi; prev=\"$arg\"; done\nprintf '%s\\n' '{\"summary\":\"Needs confirmation\",\"proposals\":[]}' > \"$out\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var output bytes.Buffer
	options := AgentOptions{RuntimeDir: runtime, Device: "fixture", Timeout: 5 * time.Second}
	if err := router.RunAgent(options, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Pending review:") {
		t.Fatalf("worker output: %s", output.String())
	}
	reports, err := filepath.Glob(filepath.Join(runtime, "reviews", "*.json"))
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports: %v %v", reports, err)
	}
	mode, err := os.Stat(reports[0])
	if err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("report mode: %v %v", mode, err)
	}
	before := output.Len()
	if err := router.RunAgent(options, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if output.Len() == before || !strings.Contains(output.String()[before:], "Unchanged review input") {
		t.Fatalf("unchanged input was not skipped: %s", output.String())
	}
	if _, err := os.Stat(filepath.Join(router.Root, "review-state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("worker wrote state into checkout")
	}
}

func TestWorkerRejectsInvalidClaimTypeWithoutState(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "inbox/candidate.md", "type: candidate\nclaim_type: inference", "Maybe X")
	runtime := t.TempDir()
	bin := t.TempDir()
	fake := filepath.Join(bin, "codex")
	script := "#!/bin/sh\nout=''\nprev=''\nfor arg in \"$@\"; do if [ \"$prev\" = \"-o\" ]; then out=\"$arg\"; fi; prev=\"$arg\"; done\nprintf '%s\\n' '{\"summary\":\"bad\",\"proposals\":[{\"source\":\"x\",\"target\":\"y\",\"claim_type\":\"bogus\",\"proposal\":\"z\",\"reason\":\"r\"}]}' > \"$out\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := router.RunAgent(AgentOptions{RuntimeDir: runtime, Device: "fixture", Timeout: 5 * time.Second}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid claim type was accepted")
	}
	if _, err := os.Stat(filepath.Join(runtime, "review-state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed worker wrote state")
	}
}

func TestProviderConfigCopiesRoutingOnly(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model=\"configured-model\"\nmodel_provider=\"custom\"\n[model_providers.custom]\nbase_url=\"https://example.invalid/v1\"\nwire_api=\"responses\"\nenv_key=\"CUSTOM_API_KEY\"\nexperimental_bearer_token=\"DO-NOT-COPY\"\n[mcp_servers.arbitrary]\ncommand=\"DO-NOT-COPY\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	args, envKey, err := CodexConnectionArgs()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "DO-NOT-COPY") || !strings.Contains(joined, `model="configured-model"`) || !strings.Contains(joined, `model_providers.custom.env_key="CUSTOM_API_KEY"`) || envKey != "CUSTOM_API_KEY" {
		t.Fatalf("provider routing leaked or omitted: %s / %s", joined, envKey)
	}
}

func TestProviderConfigAcceptsProviderTableBeforeSelector(t *testing.T) {
	home := t.TempDir()
	config := "[model_providers.custom]\nbase_url=\"https://example.invalid/v1\"\nwire_api=\"responses\"\nenv_key=\"CUSTOM_API_KEY\"\nmodel_provider=\"custom\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	args, envKey, err := CodexConnectionArgs()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `model_provider="custom"`) || !strings.Contains(joined, `model_providers.custom.base_url="https://example.invalid/v1"`) || envKey != "CUSTOM_API_KEY" {
		t.Fatalf("provider table was not retained: %s / %s", joined, envKey)
	}
}

func TestProviderConfigRejectsEnvironmentInjectionNames(t *testing.T) {
	home := t.TempDir()
	config := "model_provider=\"custom\"\n[model_providers.custom]\nenv_key=\"LD_PRELOAD\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	if _, _, err := CodexConnectionArgs(); err == nil {
		t.Fatal("environment injection variable was accepted")
	}
}

func TestShowProposalsSkipsSymlinkedReport(t *testing.T) {
	router := testRouter(t)
	runtime := t.TempDir()
	reviews := filepath.Join(runtime, "reviews")
	if err := os.Mkdir(reviews, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.json")
	if err := os.WriteFile(outside, []byte(`{"status":"pending-review","summary":"secret","proposals":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(reviews, "20261015T000000.000000000Z.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var output bytes.Buffer
	if err := router.ShowProposals(runtime, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No external review proposals") || strings.Contains(output.String(), "secret") {
		t.Fatalf("symlinked report was exposed: %s", output.String())
	}
}

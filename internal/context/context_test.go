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

	"folio/internal/dshconfig"
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
	t.Setenv("FOLIO_RUNTIME", runtime)
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

func TestValidateAgentOptionsRunnerSelection(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		options AgentOptions
		wantErr string
	}{
		{"default runner", AgentOptions{}, ""},
		{"codex", AgentOptions{Runner: "codex"}, ""},
		{"dsh", AgentOptions{Runner: "dsh"}, ""},
		{"unknown", AgentOptions{Runner: "other"}, "runner must be codex or dsh"},
		{"case sensitive", AgentOptions{Runner: "DSH"}, "runner must be codex or dsh"},
		{"padded", AgentOptions{Runner: " dsh"}, "runner must be codex or dsh"},
		{"model still validated", AgentOptions{Runner: "dsh", Model: "bad model"}, "model name is invalid"},
		{"device still validated", AgentOptions{Runner: "codex", Device: "bad\nname"}, "device name is invalid"},
	} {
		err := validateAgentOptions(testCase.options)
		if testCase.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", testCase.name, err)
			}
			continue
		}
		if err == nil || err.Error() != testCase.wantErr {
			t.Fatalf("%s: err=%v want %q", testCase.name, err, testCase.wantErr)
		}
	}
}

func TestWorkerDSHArgsAndPrompt(t *testing.T) {
	args := workerDSHArgs("")
	if strings.Join(args, " ") != "--profile headless --json -" {
		t.Fatalf("unexpected dsh args: %v", args)
	}
	for _, forbidden := range []string{"--model", "--sandbox", "--output-schema"} {
		if strings.Contains(strings.Join(args, " "), forbidden) {
			t.Fatalf("dsh args contain forbidden %q: %v", forbidden, args)
		}
	}
	patched := workerDSHArgs("/tmp/model.yml")
	if strings.Join(patched, " ") != "--profile headless --patch /tmp/model.yml --json -" {
		t.Fatalf("unexpected patched dsh args: %v", patched)
	}
	prompt := string(reviewDSHPrompt([]byte("base prompt"), proposalSchema))
	if !strings.HasPrefix(prompt, "base prompt") {
		t.Fatalf("dsh prompt did not extend the shared prompt: %q", prompt)
	}
	for _, required := range []string{"Return exactly one JSON object and nothing else. No prose, no markdown fences.", "The object must match this schema:", proposalSchema} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("dsh prompt omitted %q", required)
		}
	}
}

func TestDSHModelPatch(t *testing.T) {
	t.Setenv("DSH_MODEL_PROVIDER", "")
	provider, err := dshModelProvider("example-large", "", dshconfig.Config{})
	if err != nil || provider != "deepseek-official" {
		t.Fatalf("default provider=%q err=%v", provider, err)
	}
	patch := string(dshModelPatch("example-large", provider, dshconfig.Config{}, false))
	for _, required := range []string{"- id: agent-default-model", `provider: "deepseek-official"`, `model: "example-large"`} {
		if !strings.Contains(patch, required) {
			t.Fatalf("patch omitted %q: %s", required, patch)
		}
	}
	if _, err := dshModelProvider("", "bad provider", dshconfig.Config{}); err == nil {
		t.Fatal("invalid DSH model provider was accepted")
	}

	path := filepath.Join(t.TempDir(), "cordis.patch.yml")
	if err := os.WriteFile(path, []byte(`- id: agent-default-model
  config:
    provider: example-route
    model: example-large:1017
- id: llm-pi-ai
  config:
    providers:
      example-route:
        apiKeyEnv: EXAMPLE_API_KEY
        api: openai-responses
        baseURL: https://example.invalid/v1
        models:
          - id: example-large:1017
            name: Example Large
          - id: example-small
            name: Example Small
`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := dshconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	provider, err = dshModelProvider("example-small", "", config)
	if err != nil || provider != "example-route" {
		t.Fatalf("configured provider=%q err=%v", provider, err)
	}
	patch = string(dshModelPatch("example-small", provider, config, true))
	for _, required := range []string{
		"- id: llm-pi-ai", "example-route:", "apiKeyEnv: EXAMPLE_API_KEY",
		`provider: "example-route"`, `model: "example-small"`,
		"- id: credentials", "- id: authorization", "- id: deepseek-account",
	} {
		if !strings.Contains(patch, required) {
			t.Fatalf("configured patch omitted %q: %s", required, patch)
		}
	}
	t.Setenv("DSH_MODEL_PROVIDER", "bad provider")
	if _, err := dshModelProvider("example-small", "", config); err == nil {
		t.Fatal("invalid DSH model provider was accepted")
	}
}

func TestLimitedWriterCapsDSHOutput(t *testing.T) {
	writer := &limitedWriter{limit: 4}
	if n, err := writer.Write([]byte("abcdef")); n != 6 || err != nil || writer.buffer.String() != "abcd" || !writer.truncated {
		t.Fatalf("first write n=%d err=%v buffer=%q truncated=%t", n, err, writer.buffer.String(), writer.truncated)
	}
	if n, err := writer.Write([]byte("x")); n != 1 || err != nil || writer.buffer.Len() != 4 {
		t.Fatalf("second write n=%d err=%v len=%d", n, err, writer.buffer.Len())
	}
}

func TestWorkerDSHConfigEnvironmentIsForwarded(t *testing.T) {
	for _, key := range []string{"DSH_CONFIG_PATCH", "DSH_CONFIG_HOME", "DSH_MODEL_PROVIDER", "DSH_CREDENTIALS"} {
		if !safeEnvName(key) {
			t.Fatalf("%s was filtered from the worker environment", key)
		}
	}
	if safeEnvName("DSH_SOMETHING_ELSE") {
		t.Fatal("unrelated DSH environment was allowed")
	}
}

func TestWorkerDSHEnvForcesReadOnlyAndOverridesHome(t *testing.T) {
	base := []string{"PATH=/usr/bin", "CODEX_HOME=/data/codex", "DSH_PERMISSION_MODE=workspace-write", "DSH_HOME=/old"}
	env := workerDSHEnv(base, "/data/dsh")
	joined := strings.Join(env, "\n")
	if strings.Count(joined, "DSH_PERMISSION_MODE=") != 1 || !strings.Contains(joined, "DSH_PERMISSION_MODE=read-only") {
		t.Fatalf("permission mode was not forced: %v", env)
	}
	if strings.Count(joined, "DSH_HOME=") != 1 || !strings.Contains(joined, "DSH_HOME=/data/dsh") {
		t.Fatalf("dsh home was not overridden: %v", env)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "CODEX_HOME=/data/codex") {
		t.Fatalf("worker environment was dropped: %v", env)
	}
	if without := workerDSHEnv([]string{"PATH=/usr/bin"}, ""); strings.Contains(strings.Join(without, "\n"), "DSH_HOME=") {
		t.Fatalf("empty dsh home was forwarded: %v", without)
	}
}

func TestExtractDSHFinal(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{"single", `{"type":"final","text":"hello"}` + "\n", "hello", true},
		{"last final wins", `{"type":"progress","text":"p"}` + "\n" + `{"type":"final","text":"first"}` + "\n" + `{"type":"final","text":"second"}`, "second", true},
		{"no final", `{"type":"progress","text":"p"}`, "", false},
		{"empty text", `{"type":"final","text":""}`, "", false},
		{"whitespace text", `{"type":"final","text":"   "}`, "", false},
		{"malformed line skipped", "not json\n" + `{"type":"final","text":"ok"}`, "ok", true},
		{"empty stream", "", "", false},
	} {
		text, ok := extractDSHFinal([]byte(testCase.input))
		if text != testCase.want || ok != testCase.ok {
			t.Fatalf("%s: got (%q, %t) want (%q, %t)", testCase.name, text, ok, testCase.want, testCase.ok)
		}
	}
}

func TestExtractProposalJSON(t *testing.T) {
	valid := `{"summary":"Needs confirmation","proposals":[]}`
	badClaim := `{"summary":"s","proposals":[{"source":"a","target":"b","claim_type":"bogus","proposal":"p","reason":"r"}]}`
	for _, testCase := range []struct {
		name  string
		input string
		ok    bool
	}{
		{"plain", valid, true},
		{"fenced", "```json\n" + valid + "\n```", true},
		{"fenced without language", "```\n" + valid + "\n```\n", true},
		{"surrounding prose", "Here is the result:\n" + valid + "\nDone.", true},
		{"no object", "no json here", false},
		{"truncated object", `{"summary":"s"`, false},
		{"extra field", `{"summary":"s","proposals":[],"extra":true}`, false},
		{"invalid claim", badClaim, false},
	} {
		proposal, err := extractProposalJSON(testCase.input)
		if testCase.ok {
			if err != nil || proposal.Summary != "Needs confirmation" {
				t.Fatalf("%s: proposal=%#v err=%v", testCase.name, proposal, err)
			}
			continue
		}
		if err == nil || err.Error() != "invalid review result" {
			t.Fatalf("%s: err=%v want invalid review result", testCase.name, err)
		}
	}
}

func TestValidateProposalDataRejectsInvalidShapes(t *testing.T) {
	for _, input := range []string{
		`{}`,
		`[]`,
		`null`,
		`{"summary":"s"}`,
		`{"summary":"s","proposals":{}}`,
		`{"summary":"s","proposals":[],"extra":true}`,
		`{"summary":7,"proposals":[]}`,
		`{"summary":"s","proposals":[{"source":"a","target":"b","claim_type":"fact","proposal":"p"}]}`,
		`{"summary":"s","proposals":[{"source":"a","target":"b","claim_type":"bogus","proposal":"p","reason":"r"}]}`,
		`{"summary":"s","proposals":[{"source":"a","target":"b","claim_type":"fact","proposal":"p","reason":"r","extra":"x"}]}`,
	} {
		if _, err := validateProposalData([]byte(input)); err == nil {
			t.Fatalf("invalid proposal data was accepted: %s", input)
		}
	}
	proposal, err := validateProposalData([]byte(`{"summary":"s","proposals":[]}`))
	if err != nil || proposal.Summary != "s" || len(proposal.Proposals) != 0 {
		t.Fatalf("valid proposal data was rejected: %#v %v", proposal, err)
	}
}

func TestWorkerDSHRunnerProducesProposalOnlyReport(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "inbox/candidate.md", "type: candidate\nclaim_type: inference", "Maybe X")
	runtime := t.TempDir()
	home := t.TempDir()
	capture := filepath.Join(t.TempDir(), "dsh-capture.txt")
	finalEvent, err := json.Marshal(map[string]string{
		"type": "final",
		"text": "```json\n{\"summary\":\"Needs confirmation\",\"proposals\":[]}\n```",
	})
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fake := filepath.Join(bin, "dsh")
	script := "#!/bin/sh\n" +
		"printf 'args:%s\\n' \"$*\" > \"" + capture + "\"\n" +
		"previous=\n" +
		"patch=\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$previous\" = \"--patch\" ]; then patch=\"$arg\"; fi\n" +
		"  previous=\"$arg\"\n" +
		"done\n" +
		"if [ -n \"$patch\" ]; then printf 'patch:\\n' >> \"" + capture + "\"; cat \"$patch\" >> \"" + capture + "\"; fi\n" +
		"printf 'permission:%s\\n' \"${DSH_PERMISSION_MODE-}\" >> \"" + capture + "\"\n" +
		"printf 'home:%s\\n' \"${DSH_HOME-}\" >> \"" + capture + "\"\n" +
		"printf 'provider_key:%s\\n' \"${DEEPSEEK_API_KEY-}\" >> \"" + capture + "\"\n" +
		"printf '%s\\n' '" + string(finalEvent) + "'\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("DSH_HOME", home)
	t.Setenv("DEEPSEEK_API_KEY", "fixture-key")
	t.Setenv("DSH_MODEL_PROVIDER", "")
	var output bytes.Buffer
	options := AgentOptions{Runner: "dsh", RuntimeDir: runtime, Device: "fixture", Model: "example-large", Timeout: 5 * time.Second}
	if err := router.RunAgent(options, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Pending review:") {
		t.Fatalf("worker output: %s", output.String())
	}
	captured, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := string(captured)
	if !strings.Contains(got, "args:--profile headless --patch ") || !strings.Contains(got, " --json -") || !strings.Contains(got, "permission:read-only") || !strings.Contains(got, "home:"+home) {
		t.Fatalf("dsh invocation was not isolated: %s", got)
	}
	if !strings.Contains(got, `provider: "deepseek-official"`) || !strings.Contains(got, `model: "example-large"`) {
		t.Fatalf("dsh model patch missing: %s", got)
	}
	if !strings.Contains(got, "provider_key:\n") || strings.Contains(got, "fixture-key") {
		t.Fatalf("provider key entered the dsh environment: %s", got)
	}
	reports, err := filepath.Glob(filepath.Join(runtime, "reviews", "*.json"))
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports: %v %v", reports, err)
	}
	info, err := os.Stat(reports[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode: %v %v", info, err)
	}
	saved, err := os.ReadFile(filepath.Join(runtime, "review-state.json"))
	if err != nil || !strings.Contains(string(saved), "input_digest") {
		t.Fatalf("review state: %s %v", saved, err)
	}
	if _, err := os.Stat(filepath.Join(router.Root, "review-state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("worker wrote state into checkout")
	}
}

func TestWorkerDSHFailureModes(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "inbox/candidate.md", "type: candidate\nclaim_type: inference", "Maybe X")
	for _, testCase := range []struct {
		name   string
		script string
		want   string
	}{
		{"no final event", "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"progress\",\"text\":\"working\"}'\n", "DSH review failed; no changes applied"},
		{"empty final text", "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"final\",\"text\":\"\"}'\n", "DSH review failed; no changes applied"},
		{"invalid proposal", "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"final\",\"text\":\"not json\"}'\n", "invalid review result"},
		{"nonzero exit", "#!/bin/sh\nexit 3\n", "DSH review failed; no changes applied"},
	} {
		runtime := t.TempDir()
		bin := t.TempDir()
		fake := filepath.Join(bin, "dsh")
		if err := os.WriteFile(fake, []byte(testCase.script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		err := router.RunAgent(AgentOptions{Runner: "dsh", RuntimeDir: runtime, Device: "fixture", Timeout: 5 * time.Second}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || err.Error() != testCase.want {
			t.Fatalf("%s: err=%v want %q", testCase.name, err, testCase.want)
		}
		if _, statErr := os.Stat(filepath.Join(runtime, "review-state.json")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s: failed worker wrote state", testCase.name)
		}
	}
}

func TestWorkerDSHMissingBinary(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "inbox/candidate.md", "type: candidate\nclaim_type: inference", "Maybe X")
	t.Setenv("PATH", t.TempDir())
	err := router.RunAgent(AgentOptions{Runner: "dsh", RuntimeDir: t.TempDir(), Device: "fixture", Timeout: 5 * time.Second}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "dsh is required; install and configure the DSH headless profile first" {
		t.Fatalf("missing dsh err=%v", err)
	}
}

func TestWorkerAutoModeWritesBoundedEditReport(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "inbox/candidate.md", "type: candidate\nclaim_type: inference", "Maybe X")
	runtime := t.TempDir()
	bin := t.TempDir()
	fake := filepath.Join(bin, "codex")
	script := "#!/bin/sh\nout=''\nprev=''\nfor arg in \"$@\"; do if [ \"$prev\" = \"-o\" ]; then out=\"$arg\"; fi; prev=\"$arg\"; done\nprintf '%s\\n' '{\"summary\":\"merge\",\"edits\":[{\"source\":\"inbox/candidate.md\",\"target\":\"core/profile.md\",\"claim_type\":\"inference\",\"reason\":\"fixture\",\"content\":\"# Profile\\n\"}]}' > \"$out\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	reportPath := filepath.Join(runtime, "auto-report.json")
	var output bytes.Buffer
	if err := router.RunAgent(AgentOptions{RuntimeDir: runtime, Device: "fixture", Auto: true, AutoReport: reportPath, Timeout: 5 * time.Second}, &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report autoReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "pending-apply" || report.Summary != "merge" || len(report.Edits) != 1 || report.Edits[0].Target != "core/profile.md" {
		t.Fatalf("unexpected auto report: %#v", report)
	}
	if _, err := os.Stat(filepath.Join(runtime, "reviews")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("auto mode wrote a proposal review report")
	}
	if _, err := os.Stat(filepath.Join(router.Root, "core", "profile.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("auto worker edited the canonical checkout")
	}
	if err := router.RunAgent(AgentOptions{RuntimeDir: runtime, Device: "fixture", Auto: true, AutoReport: filepath.Join(t.TempDir(), "outside.json")}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("auto report outside the runtime was accepted")
	}
}

func TestAutoReviewInputIncludesBoundedCanonicalContent(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "core/profile.md", "type: profile\nlayer: L1", "Current profile body")
	writeDoc(t, router.Root, "inbox/candidate.md", "type: candidate\nclaim_type: inference", "Maybe a preference")
	data, err := router.ReviewInput()
	if err != nil {
		t.Fatal(err)
	}
	data, err = router.AutoReviewInput(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Canonical) == 0 {
		t.Fatal("automatic input omitted canonical content")
	}
	found := false
	for _, source := range data.Canonical {
		if source.Source == "core/profile.md" && strings.Contains(source.Body, "Current profile body") {
			found = true
		}
	}
	if !found {
		t.Fatalf("canonical profile missing: %#v", data.Canonical)
	}
	encoded, err := json.Marshal(data)
	if err != nil || len(encoded) > reviewBatchLimit {
		t.Fatalf("automatic input exceeded its budget: %d %v", len(encoded), err)
	}
}

func TestAgentCLIAutoFlagWritesReport(t *testing.T) {
	router := testRouter(t)
	writeDoc(t, router.Root, "inbox/candidate.md", "type: candidate\nclaim_type: inference", "Maybe X")
	runtime := t.TempDir()
	bin := t.TempDir()
	fake := filepath.Join(bin, "codex")
	script := "#!/bin/sh\nout=''\nprev=''\nfor arg in \"$@\"; do if [ \"$prev\" = \"-o\" ]; then out=\"$arg\"; fi; prev=\"$arg\"; done\nprintf '%s\\n' '{\"summary\":\"merge\",\"edits\":[]}' > \"$out\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	reportPath := filepath.Join(runtime, "auto-report.json")
	args := []string{"--auto", "--runtime-dir", runtime, "--device", "fixture", "--auto-report", reportPath, "--timeout", "5"}
	if err := runAgent(router, args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("agent CLI did not write the automatic report: %v", err)
	}
	args = []string{"--runtime-dir", runtime, "--auto-report", reportPath, "--timeout", "5"}
	if err := runAgent(router, args, &bytes.Buffer{}); err == nil {
		t.Fatal("agent CLI accepted --auto-report without --auto")
	}
}

package contextrepo

import "testing"

func TestParseCodexUsage(t *testing.T) {
	// Shape captured from codex-cli 0.162.1 `exec --json`.
	stdout := []byte(`{"type":"thread.started","thread_id":"t1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"{\"summary\":\"ok\"}"}}
{"type":"turn.completed","usage":{"input_tokens":14685,"cached_input_tokens":8064,"cache_write_input_tokens":0,"output_tokens":15,"reasoning_output_tokens":0}}
`)
	usage, ok := parseCodexUsage(stdout)
	if !ok {
		t.Fatal("usage was not found")
	}
	if usage.Input != 6621 || usage.Cached != 8064 || usage.Output != 15 || usage.Total != 14700 {
		t.Fatalf("usage=%+v", usage)
	}
	if usage.Source != "reported" || usage.Origin != "stdout" {
		t.Fatalf("source=%q origin=%q", usage.Source, usage.Origin)
	}
}

func TestParseCodexUsagePrefersLastEvent(t *testing.T) {
	// token_count events are cumulative, so the last one is the run total.
	stdout := []byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":10,"total_tokens":110}}}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":500,"cached_input_tokens":400,"output_tokens":30,"total_tokens":530}}}}
`)
	usage, ok := parseCodexUsage(stdout)
	if !ok {
		t.Fatal("usage was not found")
	}
	if usage.Input != 100 || usage.Cached != 400 || usage.Output != 30 || usage.Total != 530 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestParseCodexUsageIgnoresGarbage(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("not json\n"),
		[]byte(`{"type":"turn.completed","usage":{"input_tokens":0,"output_tokens":0}}`),
		[]byte(`{"type":"turn.completed","usage":{"input_tokens":-5,"output_tokens":1}}`),
	}
	for _, input := range cases {
		if _, ok := parseCodexUsage(input); ok {
			t.Fatalf("unexpected usage for %q", input)
		}
	}
}

func TestParseDSHUsageSumsRequests(t *testing.T) {
	// Each usage chunk describes one request, so the totals are sums.
	stdout := []byte(`{"type":"chunk","time":1,"chunk":{"type":"usage","usage":{"inputTokens":890,"outputTokens":698,"totalTokens":9780,"cacheReadTokens":8192}}}
{"type":"chunk","time":2,"chunk":{"type":"usage","usage":{"inputTokens":1787,"outputTokens":686,"totalTokens":11433,"cacheReadTokens":8960}}}
{"type":"final","text":"{\"summary\":\"ok\"}"}
`)
	usage, ok := parseDSHUsage(stdout)
	if !ok {
		t.Fatal("usage was not found")
	}
	if usage.Input != 2677 || usage.Cached != 17152 || usage.Output != 1384 || usage.Total != 21213 {
		t.Fatalf("usage=%+v", usage)
	}
	if usage.Requests != 2 {
		t.Fatalf("requests=%d", usage.Requests)
	}
	if _, ok := parseDSHUsage([]byte(`{"type":"final","text":"hi"}`)); ok {
		t.Fatal("usage reported without a usage chunk")
	}
}

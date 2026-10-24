package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeRunFixture(t *testing.T, store *Store, id string, started time.Time, fields map[string]any) {
	t.Helper()
	run := map[string]any{
		"version": 1, "run_id": id, "trigger": "manual", "status": "pushed",
		"started_at": started.UTC().Format(time.RFC3339Nano),
		"runner":     "codex", "summary": "x",
	}
	for key, value := range fields {
		run[key] = value
	}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.runsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.runsDir, id+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func usageFixture(input, cached, output int64) map[string]any {
	return map[string]any{
		"input_tokens": input, "cached_tokens": cached, "output_tokens": output,
		"total_tokens": input + cached + output, "source": "reported", "origin": "stdout",
	}
}

func TestUsageSummaryAggregatesDaysAndModels(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)

	writeRunFixture(t, store, "20260101T000000.000000000Z-aaaaaaaa", today, map[string]any{
		"usage": usageFixture(100, 900, 50), "model": "gpt-5",
	})
	writeRunFixture(t, store, "20260101T000001.000000000Z-bbbbbbbb", today, map[string]any{
		"usage": usageFixture(20, 0, 10), "model": "gpt-5", "runner": "dsh",
	})
	writeRunFixture(t, store, "20260101T000002.000000000Z-cccccccc", yesterday, map[string]any{
		"usage": usageFixture(7, 3, 1), "model": "gpt-5",
	})
	// A run that never called a model: counted, but not reported.
	writeRunFixture(t, store, "20260101T000003.000000000Z-dddddddd", today, map[string]any{
		"status": "skipped", "reason": "repository_not_connected",
	})
	// Outside the window.
	writeRunFixture(t, store, "20250101T000000.000000000Z-eeeeeeee", today.AddDate(0, 0, -400), map[string]any{
		"usage": usageFixture(1, 1, 1),
	})

	summary, err := store.UsageSummary(7)
	if err != nil {
		t.Fatalf("usage summary: %v", err)
	}
	totals := summary["totals"].(map[string]any)
	if totals["total_tokens"].(int64) != 1091 || totals["input_tokens"].(int64) != 127 {
		t.Fatalf("totals=%v", totals)
	}
	if totals["runs"].(int) != 4 || totals["reported_runs"].(int) != 3 || totals["unreported_runs"].(int) != 1 {
		t.Fatalf("counts=%v", totals)
	}
	days := summary["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("days=%v", days)
	}
	if days[0].(map[string]any)["date"] != yesterday.Format("2006-01-02") {
		t.Fatalf("days[0]=%v", days[0])
	}
	models := summary["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("models=%v", models)
	}
	top := models[0].(map[string]any)
	if top["runner"] != "codex" || top["total_tokens"].(int64) != 1061 {
		t.Fatalf("top model=%v", top)
	}
}

func TestUsageSummaryDefaultsTheWindow(t *testing.T) {
	store := newTestStore(t)
	summary, err := store.UsageSummary(0)
	if err != nil {
		t.Fatalf("usage summary: %v", err)
	}
	if summary["window_days"].(int) != 30 {
		t.Fatalf("window=%v", summary["window_days"])
	}
	if days, _ := summary["days"].([]any); len(days) != 0 {
		t.Fatalf("days=%v", days)
	}
}

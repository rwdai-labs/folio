package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const syntheticPricing = `{
  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
    "deepseek-v4-flash": {"id":"deepseek-v4-flash","name":"DeepSeek Flash","cost":{"input":0.15,"output":0.6,"cache_read":0.003}},
    "free-model": {"id":"free-model","name":"Free","cost":{"input":0,"output":0,"cache_read":0}}
  }},
  "openai": {"id":"openai","name":"OpenAI","models":{
    "gpt-test": {"id":"gpt-test","name":"GPT Test","cost":{"input":2,"output":10,"cache_read":0.2}}
  }}
}`

func TestPricingParseEstimateAndAlias(t *testing.T) {
	catalog, err := parseModelsDevPricing([]byte(syntheticPricing), "https://models.dev/api.json")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	store := newTestStore(t)
	store.pricingCatalog = catalog
	store.pricingURL = "https://models.dev/api.json"

	cost := store.priceUsage("deepseek", "deepseek-v4-flash", map[string]any{
		"input_tokens": float64(1_000_000), "cached_tokens": float64(1_000_000),
		"output_tokens": float64(1_000_000), "total_tokens": float64(3_000_000),
	})
	if cost["estimated"] != true || cost["total_cost"] != 0.753 {
		t.Fatalf("cost=%v", cost)
	}
	// A dated alias falls back to the base model id.
	alias := store.priceUsage("deepseek", "deepseek-v4-flash-2026-11-01", map[string]any{
		"input_tokens": float64(1_000_000), "cached_tokens": float64(0),
		"output_tokens": float64(0), "total_tokens": float64(1_000_000),
	})
	if alias["estimated"] != true || alias["total_cost"] != 0.15 {
		t.Fatalf("alias=%v", alias)
	}
	missing := store.priceUsage("deepseek", "unknown", map[string]any{
		"input_tokens": float64(1), "cached_tokens": float64(0),
		"output_tokens": float64(1), "total_tokens": float64(2),
	})
	if missing["estimated"] != false || missing["reason"] != "price_unavailable" {
		t.Fatalf("missing=%v", missing)
	}
}

func TestPricingRefreshCachesAndReloads(t *testing.T) {
	store := newTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(syntheticPricing))
	}))
	defer server.Close()
	store.pricingURL = server.URL
	store.pricingAllowHTTP = true
	status, err := store.RefreshPricing(context.Background())
	if err != nil || status["configured"] != true || status["models"] != 3 {
		t.Fatalf("refresh status=%v err=%v", status, err)
	}
	path := filepath.Join(store.dataDir, "runtime", "model-prices.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("cache stat=%v err=%v", info, err)
	}
	reloaded, err := New(store.dataDir, store.toolRoot, store.remote)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.PricingStatus()["models"] != 3 {
		t.Fatalf("reloaded status=%v", reloaded.PricingStatus())
	}
	if _, err := store.RefreshPricing(context.Background()); err == nil || err.Error() != "pricing_refresh_limited" {
		t.Fatalf("rate limit error=%v", err)
	}
}

func TestRunCostAttachesToAutomationRun(t *testing.T) {
	store := newTestStore(t)
	catalog, _ := parseModelsDevPricing([]byte(syntheticPricing), "https://models.dev/api.json")
	store.pricingCatalog = catalog
	run := map[string]any{
		"run_id": "20260101T000000.000000000Z-00000000", "runner": "codex", "model": "gpt-test",
		"status": "reported",
		"usage": map[string]any{
			"input_tokens": float64(1_000_000), "cached_tokens": float64(0),
			"output_tokens": float64(1_000_000), "total_tokens": float64(2_000_000),
			"source": "reported",
		},
	}
	cost := store.runCost(run)
	if cost["estimated"] != true || cost["total_cost"] != 12.0 {
		t.Fatalf("cost=%v", cost)
	}
	raw, _ := json.Marshal(run)
	if len(raw) == 0 {
		t.Fatal("run marshal failed")
	}
}

func TestPricingURLRejectsCredentialsAndPlainHTTP(t *testing.T) {
	for _, raw := range []string{
		"http://models.dev/api.json",
		"https://user:pass@models.dev/api.json",
		"https://models.dev/api.json?secret=1",
		"ftp://models.dev/api.json",
	} {
		if _, err := parsePricingURL(raw, false); err == nil {
			t.Fatalf("accepted unsafe pricing URL %q", raw)
		}
	}
	if _, err := parsePricingURL("https://models.dev/api.json", false); err != nil {
		t.Fatalf("valid URL rejected: %v", err)
	}
}

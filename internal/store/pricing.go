package store

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultPricingURL    = "https://models.dev/api.json"
	maxPricingBytes      = 16 * 1024 * 1024
	maxPricingFile       = 8 * 1024 * 1024
	pricingTTL           = 24 * time.Hour
	pricingRefreshGap    = 30 * time.Second
	pricingCheckInterval = 6 * time.Hour
	pricingRetryInterval = 30 * time.Minute
	pricingHTTPTimeout   = 30 * time.Second
	maxPricingProviders  = 500
	maxPricingModels     = 20000
)

var pricingDateSuffixRE = regexp.MustCompile(`-(?:[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{8})$`)

// pricingCatalog is a compact projection of the upstream model catalog. Rates
// are USD per one million tokens, matching models.dev's cost fields.
type pricingCatalog struct {
	Version   int                        `json:"version"`
	Source    string                     `json:"source"`
	SourceURL string                     `json:"source_url"`
	FetchedAt string                     `json:"fetched_at"`
	Providers map[string]pricingProvider `json:"providers"`
}

type pricingProvider struct {
	Name   string                  `json:"name,omitempty"`
	Models map[string]pricingModel `json:"models"`
}

type pricingModel struct {
	Name       string  `json:"name,omitempty"`
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

type pricingUsage struct {
	Provider string
	Model    string
	Usage    map[string]any
}

func (s *Store) loadPricingCache() {
	info, err := os.Lstat(s.pricingPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxPricingFile {
		return
	}
	raw, err := os.ReadFile(s.pricingPath)
	if err != nil || len(raw) > maxPricingFile {
		return
	}
	var catalog pricingCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil || !validPricingCatalog(&catalog) {
		return
	}
	s.pricingCatalog = &catalog
}

func validPricingCatalog(catalog *pricingCatalog) bool {
	if catalog == nil || catalog.Version != 1 || len(catalog.Providers) == 0 || len(catalog.Providers) > maxPricingProviders {
		return false
	}
	return true
}

// PricingStatus is a bounded status surface for the settings card and API.
func (s *Store) PricingStatus() map[string]any {
	s.pricingMu.Lock()
	defer s.pricingMu.Unlock()
	return s.pricingStatusLocked()
}

func (s *Store) pricingStatusLocked() map[string]any {
	result := map[string]any{
		"configured": s.pricingCatalog != nil,
		"source":     "models.dev",
		"source_url": s.pricingURL,
		"stale":      true,
		"currency":   "USD",
		"models":     0,
		"providers":  0,
	}
	if s.pricingCatalog != nil {
		models := 0
		for _, provider := range s.pricingCatalog.Providers {
			models += len(provider.Models)
		}
		result["configured"] = true
		result["source"] = s.pricingCatalog.Source
		result["source_url"] = s.pricingCatalog.SourceURL
		result["fetched_at"] = s.pricingCatalog.FetchedAt
		result["models"] = models
		result["providers"] = len(s.pricingCatalog.Providers)
		if fetched, err := time.Parse(time.RFC3339, s.pricingCatalog.FetchedAt); err == nil {
			result["stale"] = time.Since(fetched) > pricingTTL
		}
	}
	if s.pricingError != "" {
		result["error"] = s.pricingError
	}
	if !s.pricingLastAttempt.IsZero() {
		if remaining := pricingRefreshGap - time.Since(s.pricingLastAttempt); remaining > 0 {
			result["retry_after_seconds"] = int(remaining.Seconds()) + 1
		}
	}
	return result
}

// StartPricingRefresh keeps the optional catalog warm without a settings page.
// A missing or stale cache is fetched at startup; later checks use a bounded
// timer. Failures retry sooner but never block service startup, tasks or turns.
func (s *Store) StartPricingRefresh(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		for {
			delay := pricingCheckInterval
			if s.PricingStatus()["stale"] == true {
				refreshCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				status, _ := s.RefreshPricing(refreshCtx)
				cancel()
				if status["error"] != nil {
					delay = pricingRetryInterval
				}
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

// RefreshPricing downloads and parses the catalog. A previous successful
// catalog stays usable when the refresh fails.
func (s *Store) RefreshPricing(ctx context.Context) (map[string]any, error) {
	s.pricingMu.Lock()
	if !s.pricingLastAttempt.IsZero() && time.Since(s.pricingLastAttempt) < pricingRefreshGap {
		status := s.pricingStatusLocked()
		s.pricingMu.Unlock()
		return status, errStore("pricing_refresh_limited")
	}
	s.pricingLastAttempt = time.Now()
	url := s.pricingURL
	allowHTTP := s.pricingAllowHTTP
	s.pricingMu.Unlock()
	parsed, parseErr := parsePricingURL(url, allowHTTP)
	if parseErr != nil {
		s.setPricingError("pricing_url_invalid")
		return s.PricingStatus(), nil
	}
	url = parsed

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, pricingHTTPTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		s.setPricingError("pricing_unavailable")
		return s.PricingStatus(), nil
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "folio-pricing")
	client := &http.Client{
		Timeout: pricingHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		s.setPricingError("pricing_unavailable")
		return s.PricingStatus(), nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.setPricingError("pricing_unavailable")
		return s.PricingStatus(), nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxPricingBytes+1))
	if err != nil || len(raw) > maxPricingBytes {
		s.setPricingError("pricing_invalid")
		return s.PricingStatus(), nil
	}
	catalog, err := parseModelsDevPricing(raw, url)
	if err != nil {
		s.setPricingError("pricing_invalid")
		return s.PricingStatus(), nil
	}
	encoded, err := json.Marshal(catalog)
	if err != nil || len(encoded) > maxPricingFile {
		s.setPricingError("pricing_invalid")
		return s.PricingStatus(), nil
	}
	s.pricingMu.Lock()
	if err := ensurePrivateDir(filepath.Dir(s.pricingPath)); err == nil {
		err = writeAtomic(s.pricingPath, append(encoded, '\n'), 0o600)
	}
	if err != nil {
		s.pricingError = "pricing_cache_failed"
	} else {
		s.pricingCatalog = catalog
		s.pricingError = ""
	}
	status := s.pricingStatusLocked()
	s.pricingMu.Unlock()
	return status, nil
}

func parsePricingURL(raw string, allowHTTP bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errStore("pricing_url_invalid")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return "", errStore("pricing_url_invalid")
	}
	return parsed.String(), nil
}

func (s *Store) setPricingError(code string) {
	s.pricingMu.Lock()
	s.pricingError = code
	s.pricingMu.Unlock()
}

func parseModelsDevPricing(raw []byte, sourceURL string) (*pricingCatalog, error) {
	var upstream map[string]struct {
		Name   string `json:"name"`
		Models map[string]struct {
			Name string `json:"name"`
			Cost *struct {
				Input      float64 `json:"input"`
				Output     float64 `json:"output"`
				CacheRead  float64 `json:"cache_read"`
				CacheWrite float64 `json:"cache_write"`
			} `json:"cost"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, errStore("pricing_invalid")
	}
	if len(upstream) == 0 || len(upstream) > maxPricingProviders {
		return nil, errStore("pricing_invalid")
	}
	catalog := &pricingCatalog{
		Version:   1,
		Source:    "models.dev",
		SourceURL: sourceURL,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Providers: map[string]pricingProvider{},
	}
	total := 0
	for providerID, provider := range upstream {
		providerID = strings.TrimSpace(strings.ToLower(providerID))
		if providerID == "" || len(provider.Models) == 0 {
			continue
		}
		models := map[string]pricingModel{}
		for modelID, model := range provider.Models {
			modelID = strings.TrimSpace(strings.ToLower(modelID))
			if modelID == "" || model.Cost == nil {
				continue
			}
			rates := []float64{model.Cost.Input, model.Cost.Output, model.Cost.CacheRead, model.Cost.CacheWrite}
			valid := true
			for _, rate := range rates {
				if rate < 0 || rate > 1e6 {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			models[modelID] = pricingModel{
				Name: publicText(model.Name, 200), Input: model.Cost.Input,
				Output: model.Cost.Output, CacheRead: model.Cost.CacheRead,
				CacheWrite: model.Cost.CacheWrite,
			}
			total++
			if total >= maxPricingModels {
				break
			}
		}
		if len(models) > 0 {
			catalog.Providers[providerID] = pricingProvider{Name: publicText(provider.Name, 200), Models: models}
		}
		if total >= maxPricingModels {
			break
		}
	}
	if len(catalog.Providers) == 0 {
		return nil, errStore("pricing_invalid")
	}
	return catalog, nil
}

func (s *Store) pricingModel(provider, model string) (pricingModel, string, bool) {
	s.pricingMu.Lock()
	defer s.pricingMu.Unlock()
	if s.pricingCatalog == nil {
		return pricingModel{}, "", false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return pricingModel{}, "", false
	}
	if provider != "" {
		if item, ok := s.pricingCatalog.Providers[provider]; ok {
			if rate, ok := item.Models[model]; ok {
				return rate, provider, true
			}
		}
	}
	// Model ids occasionally carry a release date or a "latest" suffix.
	for _, alias := range pricingAliases(model) {
		if provider != "" {
			if item, ok := s.pricingCatalog.Providers[provider]; ok {
				if rate, ok := item.Models[alias]; ok {
					return rate, provider, true
				}
			}
		}
	}
	// Without a provider, only a unique exact match is safe.
	var found pricingModel
	var foundProvider string
	count := 0
	for id, item := range s.pricingCatalog.Providers {
		if rate, ok := item.Models[model]; ok {
			found, foundProvider, count = rate, id, count+1
		}
	}
	if count == 1 {
		return found, foundProvider, true
	}
	return pricingModel{}, "", false
}

func pricingAliases(model string) []string {
	result := []string{}
	for _, suffix := range []string{"-latest", ":latest"} {
		if strings.HasSuffix(model, suffix) {
			result = append(result, strings.TrimSuffix(model, suffix))
		}
	}
	// Date suffixes: 2026-11-01 and 20261101.
	if match := pricingDateSuffixRE.FindString(model); match != "" {
		result = append(result, strings.TrimSuffix(model, match))
	}
	return result
}

func usageInt(usage map[string]any, key string) (int64, bool) {
	value, ok := usage[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed > 1<<40 {
			return 0, false
		}
		return int64(typed), true
	case int64:
		if typed < 0 || typed > 1<<40 {
			return 0, false
		}
		return typed, true
	case int:
		if typed < 0 {
			return 0, false
		}
		return int64(typed), true
	default:
		return 0, false
	}
}

func estimatePricing(rate pricingModel, provider, model string, usage map[string]any, source string) map[string]any {
	if usage == nil {
		return nil
	}
	input, okInput := usageInt(usage, "input_tokens")
	cached, okCached := usageInt(usage, "cached_tokens")
	output, okOutput := usageInt(usage, "output_tokens")
	if !okInput || !okCached || !okOutput {
		return nil
	}
	cost := func(tokens int64, perMillion float64) float64 {
		return float64(tokens) * perMillion / 1_000_000
	}
	inputCost := cost(input, rate.Input)
	cachedCost := cost(cached, rate.CacheRead)
	if rate.CacheRead == 0 {
		cachedCost = cost(cached, rate.Input)
	}
	outputCost := cost(output, rate.Output)
	total := inputCost + cachedCost + outputCost
	round := func(value float64) float64 {
		return float64(int64(value*1e6+0.5)) / 1e6
	}
	return map[string]any{
		"estimated":       true,
		"currency":        "USD",
		"provider":        provider,
		"model":           model,
		"input_cost":      round(inputCost),
		"cached_cost":     round(cachedCost),
		"output_cost":     round(outputCost),
		"total_cost":      round(total),
		"input_rate":      rate.Input,
		"output_rate":     rate.Output,
		"cache_read_rate": rate.CacheRead,
		"source":          source,
	}
}

func (s *Store) priceUsage(provider, model string, usage map[string]any) map[string]any {
	rate, matchedProvider, ok := s.pricingModel(provider, model)
	if !ok {
		return map[string]any{
			"estimated": false, "currency": "USD", "provider": provider, "model": model,
			"reason": "price_unavailable", "source": "models.dev",
		}
	}
	result := estimatePricing(rate, matchedProvider, model, usage, "models.dev")
	if result == nil {
		return map[string]any{
			"estimated": false, "currency": "USD", "provider": provider, "model": model,
			"reason": "usage_unavailable", "source": "models.dev",
		}
	}
	return result
}

func (s *Store) runModelUsage(run map[string]any) []pricingUsage {
	if entries, ok := run["model_usage"].([]any); ok && len(entries) > 0 {
		result := make([]pricingUsage, 0, len(entries))
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			usage, _ := entry["usage"].(map[string]any)
			result = append(result, pricingUsage{
				Provider: responseString(entry, "provider"), Model: responseString(entry, "model"), Usage: usage,
			})
		}
		if len(result) > 0 {
			return result
		}
	}
	usage, _ := run["usage"].(map[string]any)
	if usage == nil {
		return nil
	}
	provider := responseString(run, "provider")
	model := responseString(run, "model")
	runner := responseString(run, "runner")
	if provider == "" {
		switch runner {
		case "codex":
			provider = "openai"
		case "dsh":
			provider = dshProviderForModel(model)
		}
	}
	return []pricingUsage{{Provider: provider, Model: model, Usage: usage}}
}

func (s *Store) runCost(run map[string]any) map[string]any {
	entries := s.runModelUsage(run)
	if len(entries) == 0 {
		return nil
	}
	if len(entries) == 1 {
		return s.priceUsage(entries[0].Provider, entries[0].Model, entries[0].Usage)
	}
	total := 0.0
	estimated := true
	parts := make([]any, 0, len(entries))
	for _, entry := range entries {
		part := s.priceUsage(entry.Provider, entry.Model, entry.Usage)
		if part == nil {
			continue
		}
		if part["estimated"] == true {
			if value, ok := part["total_cost"].(float64); ok {
				total += value
			}
		} else {
			estimated = false
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return nil
	}
	return map[string]any{
		"estimated": estimated, "currency": "USD", "total_cost": roundCost(total),
		"parts": parts, "source": "models.dev", "partial": !estimated,
	}
}

func roundCost(value float64) float64 {
	return float64(int64(value*1e6+0.5)) / 1e6
}

func (s *Store) attachRunCost(run map[string]any) {
	if cost := s.runCost(run); cost != nil {
		run["cost"] = cost
	}
}

func (s *Store) pricingModelCount() int {
	s.pricingMu.Lock()
	defer s.pricingMu.Unlock()
	if s.pricingCatalog == nil {
		return 0
	}
	count := 0
	for _, provider := range s.pricingCatalog.Providers {
		count += len(provider.Models)
	}
	return count
}

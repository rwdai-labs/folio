// Package quota reads subscription allowance cards from an optional external
// quota source and normalizes them for the authenticated dashboard.
//
// It never talks to a subscription vendor, never holds a subscription
// credential, and never changes task scheduling. The source is a local or
// private magpie gateway; the only credential it reads is a dedicated gateway
// key used to ask for quota cards.
package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	configName       = "quotas.json"
	maxConfigBytes   = 64 * 1024
	maxKeyBytes      = 4 * 1024
	maxResponseBytes = 1024 * 1024
	maxCards         = 48
	maxWindows       = 12
	maxProviderRunes = 64
	maxNameRunes     = 128
	maxAccountRunes  = 256
	maxAliasRunes    = 128
	maxKindRunes     = 32
	maxPlanRunes     = 128
	maxWindowRunes   = 64
	maxDisplayRunes  = 256
	maxUnitRunes     = 32
	maxBalanceRunes  = 256
	maxErrorRunes    = 300
	defaultTimeout   = 5 * time.Second
	defaultMaxAge    = 60 * time.Second
	minMaxAge        = 5 * time.Second
	maxMaxAge        = 3600 * time.Second
	refreshTimeout   = 20 * time.Second
	refreshInterval  = 30 * time.Second
	staleAfter       = 15 * time.Minute
)

const (
	unconfiguredCode = "unconfigured"

	// Error codes are part of the small HTTP contract. Keep them fixed: the
	// browser translates them and never receives a vendor's raw error.
	ErrInvalidConfig   = "quota_invalid"
	ErrKeyUnavailable  = "quota_key_unavailable"
	ErrUnauthorized    = "quota_unauthorized"
	ErrUnavailable     = "quota_unavailable"
	ErrInvalidResponse = "quota_invalid_response"
	ErrRateLimited     = "quota_rate_limited"
	ErrRefreshLimited  = "quota_refresh_limited"
)

// Snapshot is the bounded, normalized view returned to the browser.
type Snapshot struct {
	Configured        bool   `json:"configured"`
	Source            string `json:"source,omitempty"`
	FetchedAt         string `json:"fetched_at,omitempty"`
	Stale             bool   `json:"stale,omitempty"`
	Error             string `json:"error"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	Cards             []Card `json:"cards"`
}

// Card is one subscription, plan, or prepaid balance.
type Card struct {
	Provider      string   `json:"provider"`
	ProviderLabel string   `json:"provider_label,omitempty"`
	Account       string   `json:"account,omitempty"`
	Alias         string   `json:"alias,omitempty"`
	Kind          string   `json:"kind"`
	Plan          string   `json:"plan,omitempty"`
	Windows       []Window `json:"windows"`
	Balance       string   `json:"balance,omitempty"`
	ReadAt        string   `json:"read_at,omitempty"`
	Error         string   `json:"error,omitempty"`
	Stale         bool     `json:"stale,omitempty"`
}

// Map returns the JSON-ready bounded view. The HTTP adapter redacts maps, so
// handing it a map keeps the response on the same bounded path as every other
// API value instead of reflecting over a struct.
func (s Snapshot) Map() map[string]any {
	if s.Cards == nil {
		s.Cards = []Card{}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return map[string]any{"configured": s.Configured, "cards": []any{}}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{"configured": s.Configured, "cards": []any{}}
	}
	return out
}

// Window is one allowance window. Used and Remaining are percentages; Display
// is the vendor's own text. Text means the window has no numeric reading, so
// the browser must not render a zero-percent bar.
type Window struct {
	Name      string   `json:"name"`
	Used      float64  `json:"used"`
	Remaining float64  `json:"remaining"`
	ResetsAt  string   `json:"resets_at,omitempty"`
	Display   string   `json:"display,omitempty"`
	Unlimited bool     `json:"unlimited,omitempty"`
	Text      bool     `json:"text,omitempty"`
	Amount    float64  `json:"amount,omitempty"`
	Limit     float64  `json:"limit,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	Balance   *float64 `json:"balance,omitempty"`
}

// Service holds the in-memory snapshot cache. Config is re-read on every call
// so an owner can add or fix data/quotas.json without restarting Folio.
type Service struct {
	dataDir string

	mu          sync.Mutex
	cache       Snapshot
	cachedAt    time.Time
	cacheKey    string
	lastRefresh time.Time
}

// New creates a quota service rooted at the private data directory. The
// configuration is optional; an absent file means "not configured".
func New(dataDir string) *Service {
	return &Service{dataDir: dataDir}
}

// Snapshot returns the current cards. refresh asks magpie to read the vendors
// first, then returns the refreshed cards. A source failure is encoded in
// Snapshot.Error; cached cards are returned with Stale=true when available.
func (s *Service) Snapshot(ctx context.Context, refresh bool) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, code := loadConfig(s.dataDir)
	switch code {
	case "":
	case unconfiguredCode:
		s.resetCache()
		return Snapshot{Configured: false, Cards: []Card{}}
	default:
		s.resetCache()
		return Snapshot{Configured: true, Error: code, Cards: []Card{}}
	}

	if cfg.cacheKey != s.cacheKey {
		s.cacheKey = cfg.cacheKey
		s.cache = Snapshot{}
		s.cachedAt = time.Time{}
		s.lastRefresh = time.Time{}
	}

	if refresh {
		if !s.lastRefresh.IsZero() {
			if wait := refreshInterval - time.Since(s.lastRefresh); wait > 0 {
				out := s.cachedSnapshot()
				out.Error = ErrRefreshLimited
				out.RetryAfterSeconds = retrySeconds(wait)
				return out
			}
		}
		s.lastRefresh = time.Now()
		return s.finish(cfg, s.fetch(ctx, cfg, true))
	}

	if !s.cachedAt.IsZero() && time.Since(s.cachedAt) < cfg.maxAge {
		return s.cachedSnapshot()
	}
	return s.finish(cfg, s.fetch(ctx, cfg, false))
}

func (s *Service) resetCache() {
	s.cache = Snapshot{}
	s.cachedAt = time.Time{}
	s.cacheKey = ""
	s.lastRefresh = time.Time{}
}

func (s *Service) cachedSnapshot() Snapshot {
	out := s.cache
	out.Configured = true
	out.Source = "magpie"
	if out.Cards == nil {
		out.Cards = []Card{}
	}
	return out
}

func (s *Service) finish(cfg source, result fetchResult) Snapshot {
	if result.errCode == "" {
		cards := result.cards
		if cards == nil {
			cards = []Card{}
		}
		s.cache = Snapshot{
			Configured: true,
			Source:     "magpie",
			FetchedAt:  time.Now().UTC().Format(time.RFC3339),
			Cards:      cards,
		}
		s.cachedAt = time.Now()
		return s.cache
	}

	out := s.cachedSnapshot()
	out.Error = result.errCode
	out.RetryAfterSeconds = result.retryAfter
	out.Stale = len(out.Cards) > 0
	return out
}

type source struct {
	baseURL  string
	keyPath  string
	timeout  time.Duration
	maxAge   time.Duration
	cacheKey string
}

type configFile struct {
	Version int `json:"version"`
	Source  struct {
		Kind           string `json:"kind"`
		URL            string `json:"url"`
		KeyFile        string `json:"key_file"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		MaxAgeSeconds  int    `json:"max_age_seconds"`
	} `json:"source"`
}

func loadConfig(dataDir string) (source, string) {
	path := filepath.Join(dataDir, configName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return source{}, unconfiguredCode
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return source{}, ErrInvalidConfig
	}
	raw, err := os.ReadFile(path)
	if err != nil || int64(len(raw)) > maxConfigBytes {
		return source{}, ErrInvalidConfig
	}

	var file configFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return source{}, ErrInvalidConfig
	}
	if file.Version != 1 || strings.TrimSpace(file.Source.Kind) != "magpie" {
		return source{}, ErrInvalidConfig
	}

	base, ok := normalizeBaseURL(file.Source.URL)
	if !ok {
		return source{}, ErrInvalidConfig
	}

	keyFile := strings.TrimSpace(file.Source.KeyFile)
	if keyFile == "" {
		return source{}, ErrInvalidConfig
	}
	keyFile = filepath.Clean(keyFile)
	if !filepath.IsLocal(keyFile) || keyFile == "." {
		return source{}, ErrInvalidConfig
	}

	timeout := defaultTimeout
	if file.Source.TimeoutSeconds != 0 {
		if file.Source.TimeoutSeconds < 1 || file.Source.TimeoutSeconds > 30 {
			return source{}, ErrInvalidConfig
		}
		timeout = time.Duration(file.Source.TimeoutSeconds) * time.Second
	}
	maxAge := defaultMaxAge
	if file.Source.MaxAgeSeconds != 0 {
		if file.Source.MaxAgeSeconds < int(minMaxAge/time.Second) || file.Source.MaxAgeSeconds > int(maxMaxAge/time.Second) {
			return source{}, ErrInvalidConfig
		}
		maxAge = time.Duration(file.Source.MaxAgeSeconds) * time.Second
	}

	keyPath := ""
	if keyFile != "" {
		keyPath = filepath.Join(dataDir, keyFile)
	}
	return source{
		baseURL:  base,
		keyPath:  keyPath,
		timeout:  timeout,
		maxAge:   maxAge,
		cacheKey: base + "\x00" + keyPath + "\x00" + maxAge.String(),
	}, ""
}

func normalizeBaseURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", false
	}
	host := parsed.Hostname()
	if host == "" || strings.ContainsAny(host, " \t\r\n/\\") {
		return "", false
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", false
		}
	}
	return scheme + "://" + parsed.Host, true
}

type rawCard struct {
	Provider string      `json:"provider"`
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Plan     string      `json:"plan"`
	User     string      `json:"user"`
	Alias    string      `json:"alias"`
	Windows  []rawWindow `json:"windows"`
	Balance  string      `json:"balance"`
	ReadAt   *time.Time  `json:"readAt"`
	Error    string      `json:"error"`
}

type rawWindow struct {
	Name      string     `json:"name"`
	Used      float64    `json:"used"`
	Remaining float64    `json:"remaining"`
	ResetsAt  *time.Time `json:"resetsAt"`
	Display   string     `json:"display"`
	Unlimited bool       `json:"unlimited"`
	Text      bool       `json:"text"`
	Amount    float64    `json:"amount"`
	Limit     float64    `json:"limit"`
	Unit      string     `json:"unit"`
	Balance   *float64   `json:"balance"`
}

type fetchResult struct {
	cards      []Card
	errCode    string
	retryAfter int
}

func (s *Service) fetch(ctx context.Context, cfg source, refresh bool) fetchResult {
	if refresh {
		raw, status, _, code, retry := s.request(ctx, cfg, http.MethodPost, "/v1/magpie/quotas/refresh", refreshRequestTimeout(cfg.timeout))
		if code != "" {
			return fetchResult{errCode: code, retryAfter: retry}
		}
		if status != http.StatusNoContent && len(bytes.TrimSpace(raw)) > 0 {
			if cards, parseCode := parseCards(raw); parseCode == "" {
				return fetchResult{cards: cards}
			}
		}
	}

	raw, _, _, code, retry := s.request(ctx, cfg, http.MethodGet, "/v1/magpie/quotas?kept=1", cfg.timeout)
	if code != "" {
		return fetchResult{errCode: code, retryAfter: retry}
	}
	cards, code := parseCards(raw)
	if code != "" {
		return fetchResult{errCode: code}
	}
	return fetchResult{cards: cards}
}

func (s *Service) request(ctx context.Context, cfg source, method, path string, timeout time.Duration) ([]byte, int, http.Header, string, int) {
	key, code := readKey(cfg)
	if code != "" {
		return nil, 0, nil, code, 0
	}

	req, err := http.NewRequestWithContext(ctx, method, cfg.baseURL+path, nil)
	if err != nil {
		return nil, 0, nil, ErrUnavailable, 0
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, nil, ErrUnavailable, 0
	}
	defer resp.Body.Close()

	retry := parseRetryAfter(resp.Header.Get("Retry-After"))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, resp.StatusCode, resp.Header, ErrUnauthorized, retry
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, resp.StatusCode, resp.Header, ErrRateLimited, retry
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, resp.StatusCode, resp.Header, ErrUnavailable, retry
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, resp.Header, ErrUnavailable, retry
	}
	if int64(len(raw)) > maxResponseBytes {
		return nil, resp.StatusCode, resp.Header, ErrInvalidResponse, retry
	}
	return raw, resp.StatusCode, resp.Header, "", retry
}

func readKey(cfg source) (string, string) {
	if cfg.keyPath == "" {
		return "", ""
	}
	info, err := os.Lstat(cfg.keyPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxKeyBytes {
		return "", ErrKeyUnavailable
	}
	raw, err := os.ReadFile(cfg.keyPath)
	if err != nil || int64(len(raw)) > maxKeyBytes {
		return "", ErrKeyUnavailable
	}
	key := strings.TrimSpace(string(raw))
	if key == "" || len(key) > maxKeyBytes || strings.ContainsAny(key, "\r\n\x00") {
		return "", ErrKeyUnavailable
	}
	return key, ""
}

func parseCards(raw []byte) ([]Card, string) {
	var envelope struct {
		Data []rawCard `json:"data"`
	}
	var items []rawCard
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Data != nil {
		items = envelope.Data
	} else {
		var list []rawCard
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, ErrInvalidResponse
		}
		items = list
	}
	if items == nil {
		return nil, ErrInvalidResponse
	}
	if len(items) > maxCards {
		items = items[:maxCards]
	}

	cards := make([]Card, 0, len(items))
	for _, item := range items {
		provider := cleanText(item.Provider, maxProviderRunes)
		label := cleanText(item.Name, maxNameRunes)
		if provider == "" && label == "" {
			continue
		}
		if label == "" {
			label = provider
		}
		kind := cleanText(item.Kind, maxKindRunes)
		if kind == "" {
			kind = "subscription"
		}
		card := Card{
			Provider:      provider,
			ProviderLabel: label,
			Account:       cleanText(item.User, maxAccountRunes),
			Alias:         cleanText(item.Alias, maxAliasRunes),
			Kind:          kind,
			Plan:          cleanText(item.Plan, maxPlanRunes),
			Balance:       cleanText(item.Balance, maxBalanceRunes),
			Error:         cleanText(item.Error, maxErrorRunes),
			Windows:       []Window{},
		}
		if item.ReadAt != nil {
			card.ReadAt = item.ReadAt.UTC().Format(time.RFC3339)
			card.Stale = time.Since(*item.ReadAt) > staleAfter
		} else {
			card.Stale = true
		}

		for _, window := range item.Windows {
			if len(card.Windows) >= maxWindows {
				break
			}
			normalized := Window{
				Name:      cleanText(window.Name, maxWindowRunes),
				Used:      clampPercent(window.Used),
				Remaining: clampPercent(window.Remaining),
				Display:   cleanText(window.Display, maxDisplayRunes),
				Unlimited: window.Unlimited,
				Text:      window.Text,
				Amount:    clampAmount(window.Amount),
				Limit:     clampAmount(window.Limit),
				Unit:      cleanText(window.Unit, maxUnitRunes),
			}
			if window.ResetsAt != nil {
				normalized.ResetsAt = window.ResetsAt.UTC().Format(time.RFC3339)
			}
			if window.Balance != nil {
				balance := clampAmount(*window.Balance)
				normalized.Balance = &balance
			}
			card.Windows = append(card.Windows, normalized)
		}
		cards = append(cards, card)
	}

	sort.SliceStable(cards, func(i, j int) bool {
		left, right := cards[i], cards[j]
		if kindRank(left.Kind) != kindRank(right.Kind) {
			return kindRank(left.Kind) < kindRank(right.Kind)
		}
		if left.ProviderLabel != right.ProviderLabel {
			return left.ProviderLabel < right.ProviderLabel
		}
		if left.Account != right.Account {
			return left.Account < right.Account
		}
		return left.Plan < right.Plan
	})
	return cards, ""
}

func kindRank(kind string) int {
	switch kind {
	case "subscription":
		return 0
	case "plan":
		return 1
	case "balance":
		return 2
	default:
		return 3
	}
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Max(0, math.Min(100, value))
}

func clampAmount(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Max(0, math.Min(value, 1e12))
}

func cleanText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if value == "" || maxRunes <= 0 {
		return ""
	}
	var out strings.Builder
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		if count >= maxRunes {
			break
		}
		out.WriteRune(r)
		count++
	}
	return strings.TrimSpace(out.String())
}

func parseRetryAfter(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return boundedRetry(seconds)
	}
	if when, err := http.ParseTime(value); err == nil {
		return retrySeconds(time.Until(when))
	}
	return 0
}

func retrySeconds(value time.Duration) int {
	if value <= 0 {
		return 1
	}
	seconds := int(math.Ceil(value.Seconds()))
	if seconds < 1 {
		return 1
	}
	if seconds > 3600 {
		return 3600
	}
	return seconds
}

func boundedRetry(seconds int) int {
	if seconds < 1 {
		return 1
	}
	if seconds > 3600 {
		return 3600
	}
	return seconds
}

func refreshRequestTimeout(base time.Duration) time.Duration {
	if base < refreshTimeout {
		return refreshTimeout
	}
	return base
}

package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	barkName          = "bark"
	barkDefaultServer = "https://api.day.app"
	barkTimeout       = 10 * time.Second
	barkMaxResponse   = 64 * 1024
)

// Bark device keys are URL-safe tokens issued by the server or the iOS app.
var barkKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// BarkConfig is the durable configuration of one Bark destination.
type BarkConfig struct {
	Server    string
	DeviceKey string
	Group     string
	Sound     string
	Level     string
}

// ValidateBark normalizes a configuration. An empty device key is allowed here
// so a partially configured channel can still be stored; NewBark requires it.
func ValidateBark(cfg BarkConfig) (BarkConfig, error) {
	key := strings.TrimSpace(cfg.DeviceKey)
	if key != "" && !barkKeyRE.MatchString(key) {
		return BarkConfig{}, ErrConfig
	}
	server, err := normalizeBarkServer(cfg.Server)
	if err != nil {
		return BarkConfig{}, err
	}
	level, ok := ParseLevel(cfg.Level)
	if !ok {
		return BarkConfig{}, ErrConfig
	}
	return BarkConfig{
		Server:    server,
		DeviceKey: key,
		Group:     ClampRunes(strings.TrimSpace(cfg.Group), MaxGroupRunes),
		Sound:     ClampRunes(strings.TrimSpace(cfg.Sound), MaxGroupRunes),
		Level:     string(level),
	}, nil
}

// Bark delivers iOS push notifications through a Bark server.
type Bark struct {
	cfg    BarkConfig
	client *http.Client
}

// NewBark validates the configuration and returns a ready channel.
func NewBark(cfg BarkConfig) (*Bark, error) {
	normalized, err := ValidateBark(cfg)
	if err != nil {
		return nil, err
	}
	if normalized.DeviceKey == "" {
		return nil, ErrConfig
	}
	return &Bark{
		cfg: normalized,
		client: &http.Client{
			Timeout: barkTimeout,
			// A redirect would forward the device key to whatever host the
			// server names. Refuse it and let the owner fix the server URL.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Name implements Channel.
func (b *Bark) Name() string { return barkName }

// Send implements Channel.
func (b *Bark) Send(ctx context.Context, m Message) Receipt {
	m = m.Normalize()
	level := m.Level
	if level == "" {
		// The stored channel level is the default; a message may override it.
		level, _ = ParseLevel(b.cfg.Level)
	}
	payload := map[string]any{
		"device_key": b.cfg.DeviceKey,
		"title":      m.Title,
		"body":       m.Body,
		"level":      string(level),
		// Keep pushes in the Bark history; the device decides retention.
		"isArchive": true,
	}
	if group := firstNonEmpty(m.Group, b.cfg.Group); group != "" {
		payload["group"] = group
	}
	if b.cfg.Sound != "" {
		payload["sound"] = b.cfg.Sound
	}
	if m.URL != "" {
		payload["url"] = m.URL
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Failed(barkName, "encode_failed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.Server+"/push", bytes.NewReader(raw))
	if err != nil {
		return Failed(barkName, "request_failed")
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := b.client.Do(request)
	if err != nil {
		return Failed(barkName, transportDetail(err))
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, barkMaxResponse))
	if response.StatusCode != http.StatusOK {
		return Failed(barkName, "http_"+strconv.Itoa(response.StatusCode))
	}
	var result struct {
		Code int `json:"code"`
	}
	// Bark signals application errors with HTTP 200 and a non-200 code, so the
	// status line alone is not enough.
	if err := json.Unmarshal(body, &result); err != nil {
		return Failed(barkName, "invalid_response")
	}
	if result.Code != http.StatusOK {
		return Failed(barkName, "code_"+strconv.Itoa(result.Code))
	}
	return Delivered(barkName)
}

func normalizeBarkServer(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = barkDefaultServer
	}
	if len(raw) > MaxURLBytes {
		return "", ErrConfig
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrConfig
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "https":
	case "http":
		// A device key sent in clear text over the public internet is a
		// credential leak, so plain HTTP is limited to local destinations.
		if !privateHost(parsed.Hostname()) {
			return "", ErrConfig
		}
	default:
		return "", ErrConfig
	}
	parsed.Scheme = scheme
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// privateHost reports whether a host is a loopback, private-network, or
// local-only name.
func privateHost(host string) bool {
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	if name == "" {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".lan", ".internal", ".home.arpa"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	if name == "localhost" {
		return true
	}
	if ip := net.ParseIP(name); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return false
}

func transportDetail(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "network"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

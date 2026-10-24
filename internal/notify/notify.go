// Package notify delivers bounded, outbound messages to push channels.
//
// Every channel in this package is outbound-only: it opens an HTTPS request to
// a server the owner configured and never accepts inbound traffic. Adding a
// channel therefore cannot widen the service's attack surface.
//
// Two rules hold for every implementation:
//
//   - The message is bounded before it leaves the process, so a long model
//     summary cannot be rejected (or truncated unpredictably) by the server.
//   - A credential never appears in a Receipt. Receipts are persisted and
//     shown in the Web UI, so they are treated as public within the device.
package notify

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxTitleRunes bounds a push title.
	MaxTitleRunes = 120
	// MaxBodyBytes bounds a push body. Push servers reject large payloads and
	// phones truncate them, so the sender decides where to cut.
	MaxBodyBytes = 4000
	// MaxGroupRunes bounds the notification group a client displays.
	MaxGroupRunes = 64
	// MaxDetailRunes bounds the failure detail recorded for a delivery.
	MaxDetailRunes = 200
	// MaxURLBytes bounds the tap target attached to a notification.
	MaxURLBytes = 2048
)

// Delivery status values recorded in the ledger.
const (
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
)

// Level is a delivery urgency understood by supported push clients.
type Level string

const (
	// LevelPassive is silent: it appears in the notification center only.
	LevelPassive Level = "passive"
	// LevelActive is the normal, sound-playing delivery.
	LevelActive Level = "active"
	// LevelTimeSensitive breaks focus modes. It needs no entitlement.
	LevelTimeSensitive Level = "timeSensitive"
)

// ErrConfig reports a channel configuration that cannot be used safely.
var ErrConfig = errors.New("invalid channel configuration")

// ParseLevel accepts only the levels shipped by supported clients. An empty
// value means the client default (active).
func ParseLevel(raw string) (Level, bool) {
	switch Level(strings.TrimSpace(raw)) {
	case LevelPassive:
		return LevelPassive, true
	case LevelActive, "":
		return LevelActive, true
	case LevelTimeSensitive:
		return LevelTimeSensitive, true
	default:
		return "", false
	}
}

// Message is one bounded push notification.
type Message struct {
	Title string
	Body  string
	Level Level
	Group string
	URL   string
}

// Normalize applies the package bounds. It is exported so callers can render
// the exact text that will be delivered in a preview.
func (m Message) Normalize() Message {
	m.Title = ClampRunes(strings.TrimSpace(m.Title), MaxTitleRunes)
	m.Group = ClampRunes(strings.TrimSpace(m.Group), MaxGroupRunes)
	m.Body = ClampBytes(strings.TrimSpace(m.Body), MaxBodyBytes)
	// An empty level means "use the channel default"; an unknown level means
	// the caller made a mistake, so it degrades to the safest visible value.
	if raw := strings.TrimSpace(string(m.Level)); raw == "" {
		m.Level = ""
	} else if level, ok := ParseLevel(raw); ok {
		m.Level = level
	} else {
		m.Level = LevelActive
	}
	if m.Title == "" {
		m.Title = "Folio"
	}
	m.URL = SafeURL(m.URL)
	return m
}

// Receipt is the outcome of one delivery attempt. Detail is a short, stable
// machine code such as "timeout" or "code_400", never raw response text.
type Receipt struct {
	Channel string `json:"channel"`
	Status  string `json:"status"`
	At      string `json:"at"`
	Detail  string `json:"detail,omitempty"`
}

// Delivered reports a successful delivery.
func Delivered(channel string) Receipt {
	return Receipt{Channel: channel, Status: StatusDelivered, At: nowStamp()}
}

// Failed reports a failed delivery with a bounded machine code.
func Failed(channel, detail string) Receipt {
	return Receipt{
		Channel: channel,
		Status:  StatusFailed,
		At:      nowStamp(),
		Detail:  ClampRunes(strings.TrimSpace(detail), MaxDetailRunes),
	}
}

// Channel delivers messages to one configured destination.
type Channel interface {
	// Name is a stable identifier used in the delivery ledger.
	Name() string
	// Send delivers one message. It must respect ctx and must not retry.
	Send(ctx context.Context, m Message) Receipt
}

// SafeURL returns the URL when it is a plain http(s) address without embedded
// credentials, and an empty string otherwise. Callers attach the result to a
// notification, so an unusable value is dropped rather than rejected.
func SafeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxURLBytes {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.String()
	default:
		return ""
	}
}

// ClampRunes trims a string to at most limit runes without splitting one.
func ClampRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

// ClampBytes trims a string to at most limit bytes on a rune boundary.
func ClampBytes(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func nowStamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

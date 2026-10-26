// Package weixin implements the public Tencent iLink protocol without an
// OpenClaw runtime. See docs/weixin-channel.md for the pinned protocol sources.
package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://ilinkai.weixin.qq.com"
	// Wire version of the reference client; bot_agent identifies Folio honestly.
	protocolVersion  = "2.4.10"
	maxResponseBytes = 4 * 1024 * 1024
	maxTextBytes     = 16 * 1024
)

var (
	ErrProtocol       = errors.New("weixin: invalid protocol response")
	ErrUnavailable    = errors.New("weixin: request unavailable")
	ErrSessionExpired = errors.New("weixin: login expired; stop service and log in again")
	ErrState          = errors.New("weixin: private state unavailable or invalid")
)

type Credentials struct {
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
	BaseURL   string `json:"base_url"`
}

type Client struct {
	http    *http.Client
	baseURL string
	token   string
}

func validBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "ilinkai.weixin.qq.com" || strings.HasSuffix(host, ".ilinkai.weixin.qq.com")
}

func NewClient(creds Credentials) (*Client, error) {
	if !validBaseURL(creds.BaseURL) || !validOpaque(creds.Token, 8192) || !validOpaque(creds.AccountID, 512) || !validOpaque(creds.UserID, 512) {
		return nil, ErrState
	}
	return newClient(creds.BaseURL, creds.Token), nil
}

func newClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), token: token,
		http: &http.Client{Timeout: 65 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func validOpaque(s string, max int) bool {
	if s == "" || len(s) > max || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// request returns only fixed diagnostic classes; even malformed bodies may
// contain tokens, QR secrets, private messages or upstream HTML diagnostics.
func (c *Client) request(ctx context.Context, endpoint string, body map[string]any, auth bool, timeout time.Duration, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var data []byte
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
		if auth {
			body["base_info"] = map[string]string{"channel_version": protocolVersion, "bot_agent": "Folio/0.1.0"}
		}
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return ErrProtocol
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/ilink/bot/"+endpoint, bytes.NewReader(data))
	if err != nil {
		return ErrProtocol
	}
	req.Header.Set("iLink-App-Id", "bot")
	req.Header.Set("iLink-App-ClientVersion", strconv.Itoa(2<<16|4<<8|10))
	if body != nil {
		uin := make([]byte, 4)
		if _, err := rand.Read(uin); err != nil {
			return ErrUnavailable
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		req.Header.Set("X-WECHAT-UIN", base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(binary.BigEndian.Uint32(uin)), 10))))
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrSessionExpired
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return ErrUnavailable
	}
	if len(raw) > maxResponseBytes {
		return ErrProtocol
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return ErrProtocol
	}
	var status struct {
		Ret     int `json:"ret"`
		ErrCode int `json:"errcode"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return ErrProtocol
	}
	if status.Ret == -14 || status.ErrCode == -14 {
		return ErrSessionExpired
	}
	if status.Ret != 0 || status.ErrCode != 0 {
		return ErrUnavailable
	}
	if json.Unmarshal(raw, out) != nil {
		return ErrProtocol
	}
	return nil
}

// wireID accepts the string used by current source and the numeric form shown
// in the public protocol document, without losing precision through float64.
type wireID string

func (id *wireID) UnmarshalJSON(raw []byte) error {
	var s string
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &s) != nil {
			return ErrProtocol
		}
	} else {
		if len(raw) == 0 || len(raw) > 128 {
			return ErrProtocol
		}
		for _, b := range raw {
			if b < '0' || b > '9' {
				return ErrProtocol
			}
		}
		s = string(raw)
	}
	if !validOpaque(s, 512) {
		return ErrProtocol
	}
	*id = wireID(s)
	return nil
}

type textItem struct {
	Text string `json:"text"`
}
type item struct {
	Type int       `json:"type"`
	Text *textItem `json:"text_item,omitempty"`
}
type wireMessage struct {
	ID           wireID `json:"message_id"`
	From         string `json:"from_user_id"`
	To           string `json:"to_user_id"`
	Session      string `json:"session_id"`
	Group        string `json:"group_id"`
	Type         int    `json:"message_type"`
	State        int    `json:"message_state"`
	Items        []item `json:"item_list"`
	ContextToken string `json:"context_token"`
}
type updates struct {
	Messages  []wireMessage `json:"msgs"`
	Cursor    string        `json:"get_updates_buf"`
	TimeoutMS int           `json:"longpolling_timeout_ms"`
}

func (c *Client) getUpdates(ctx context.Context, cursor string, timeout time.Duration) (updates, error) {
	var result updates
	err := c.request(ctx, "getupdates", map[string]any{"get_updates_buf": cursor}, true, timeout, &result)
	return result, err
}

func (c *Client) sendText(ctx context.Context, to, token, clientID, text string) error {
	if !validOpaque(to, 512) || !validOpaque(token, 8192) || text == "" || len(text) > maxTextBytes {
		return ErrProtocol
	}
	return c.request(ctx, "sendmessage", map[string]any{"msg": map[string]any{
		"from_user_id": "", "to_user_id": to, "client_id": clientID,
		"message_type": 2, "message_state": 2, "context_token": token,
		"item_list": []item{{Type: 1, Text: &textItem{Text: text}}},
	}}, true, 15*time.Second, &struct{}{})
}

// Typing is a best-effort protocol primitive for later reply-progress wiring.
// Tickets remain local to this call; they never reach the application handler.
func (c *Client) Typing(ctx context.Context, user, token string, active bool) error {
	if !validOpaque(user, 512) || !validOpaque(token, 8192) {
		return ErrProtocol
	}
	var config struct {
		Ticket string `json:"typing_ticket"`
	}
	if err := c.request(ctx, "getconfig", map[string]any{"ilink_user_id": user, "context_token": token}, true, 10*time.Second, &config); err != nil {
		return err
	}
	if !validOpaque(config.Ticket, 8192) {
		return ErrProtocol
	}
	status := 2
	if active {
		status = 1
	}
	return c.request(ctx, "sendtyping", map[string]any{"ilink_user_id": user, "typing_ticket": config.Ticket, "status": status}, true, 10*time.Second, &struct{}{})
}

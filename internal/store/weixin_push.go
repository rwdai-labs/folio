package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"folio/internal/notify"
)

// WeixinSender delivers one bounded text message to the confirmed WeChat owner.
// The transport owns the context token and the protocol; the store only decides
// when a notification is worth sending and records the outcome.
type WeixinSender interface {
	SendWeixinText(ctx context.Context, text string) error
}

// SetWeixinSender wires the outbound transport. It is called once during
// startup, before the server accepts requests; the dedicated lock keeps readers
// independent from the notification settings lock.
func (s *Store) SetWeixinSender(sender WeixinSender) {
	s.senderMu.Lock()
	defer s.senderMu.Unlock()
	s.weixinSender = sender
}

func (s *Store) weixinSenderLocked() WeixinSender {
	s.senderMu.RLock()
	defer s.senderMu.RUnlock()
	return s.weixinSender
}

// weixinSettings is the persisted switch for proactive WeChat notifications.
// The device-facing channel settings never expose a token: the transport keeps
// its own reply context beside the credentials.
type weixinSettings struct {
	Enabled        bool   `json:"enabled"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	LastTestAt     string `json:"last_test_at,omitempty"`
	LastTestStatus string `json:"last_test_status,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

// SaveWeixinPush switches proactive notifications. Enabling without a wired
// transport is rejected: the switch must never claim a delivery path that does
// not exist in this build.
func (s *Store) SaveWeixinPush(enabled bool) (map[string]any, error) {
	if enabled && s.weixinSenderLocked() == nil {
		return nil, errStore("weixin_unavailable")
	}
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	settings, err := s.loadChannelSettingsLocked()
	if err != nil {
		return nil, err
	}
	settings.Weixin.Enabled = enabled
	settings.Weixin.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.saveChannelSettingsLocked(settings); err != nil {
		return nil, err
	}
	return s.channelStatePayload(settings), nil
}

// TestWeixinPush delivers one fixed message through the stored owner context.
// A failure is reported in the receipt, not as an error.
func (s *Store) TestWeixinPush() (map[string]any, error) {
	sender := s.weixinSenderLocked()
	if sender == nil {
		return nil, errStore("weixin_unavailable")
	}
	settings, err := s.loadChannelSettings()
	if err != nil {
		return nil, err
	}
	message := weixinTestNotification(settings.Language).Normalize()
	ctx, cancel := context.WithTimeout(context.Background(), testNotifyTimeout)
	defer cancel()
	receipt := weixinReceipt(sender.SendWeixinText(ctx, weixinText(message)))
	s.recordDelivery("test", receipt, "", message.Title)
	updated, err := s.updateWeixinTestResult(receipt)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"receipt": map[string]any{
			"channel": receipt.Channel,
			"status":  receipt.Status,
			"at":      receipt.At,
			"detail":  receipt.Detail,
		},
		"state": s.channelStatePayload(updated),
	}, nil
}

func (s *Store) updateWeixinTestResult(receipt notify.Receipt) (channelSettings, error) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	settings, err := s.loadChannelSettingsLocked()
	if err != nil {
		return channelSettings{}, err
	}
	settings.Weixin.LastTestAt = receipt.At
	settings.Weixin.LastTestStatus = receipt.Status
	settings.Weixin.LastError = receipt.Detail
	if err := s.saveChannelSettingsLocked(settings); err != nil {
		return channelSettings{}, err
	}
	return settings, nil
}

// weixinText renders a notification as one bounded text message.
func weixinText(message notify.Message) string {
	message = message.Normalize()
	if message.Title == "" || message.Title == "Folio" {
		return message.Body
	}
	return notify.ClampBytes(message.Title+"\n"+message.Body, notify.MaxBodyBytes)
}

// weixinReceipt maps a transport error to a receipt with a fixed detail code.
// Raw transport text never enters the ledger.
func weixinReceipt(err error) notify.Receipt {
	if err == nil {
		return notify.Delivered("weixin")
	}
	return notify.Failed("weixin", weixinFailureDetail(err))
}

func weixinFailureDetail(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	}
	text := err.Error()
	for _, entry := range []struct{ match, code string }{
		{"disabled by deployment", "deployment_off"},
		{"explicit owner is required", "owner_required"},
		{"no reply context", "no_context"},
		{"login has not completed", "not_configured"},
	} {
		if strings.Contains(text, entry.match) {
			return entry.code
		}
	}
	return "failed"
}

func weixinTestNotification(language string) notify.Message {
	if languageOrDefault(language) == "zh" {
		return notify.Message{
			Title: "Folio 微信推送",
			Body:  "微信推送已生效。维护完成或失败时会发送到这里。",
		}
	}
	return notify.Message{
		Title: "Folio WeChat push",
		Body:  "WeChat push works. Maintenance results will arrive here.",
	}
}

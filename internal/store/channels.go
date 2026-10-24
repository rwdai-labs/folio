package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"folio/internal/notify"
)

const (
	maxChannelConfigBytes = 64 * 1024
	maxDeliveryLogBytes   = 512 * 1024
	maxDeliveryRecords    = 200
	maxDeliveryList       = 20
	notifyThrottle        = 30 * time.Second
	testNotifyTimeout     = 15 * time.Second
	pushNotifyTimeout     = 20 * time.Second
)

// channelSettings is the durable, non-public configuration of outbound push
// channels. It lives under the private volume and is never returned verbatim:
// the device key is replaced by a "configured" flag.
type channelSettings struct {
	Language string       `json:"language,omitempty"`
	Bark     barkSettings `json:"bark"`
}

type barkSettings struct {
	Enabled        bool   `json:"enabled"`
	Server         string `json:"server,omitempty"`
	DeviceKey      string `json:"device_key,omitempty"`
	Group          string `json:"group,omitempty"`
	Sound          string `json:"sound,omitempty"`
	Level          string `json:"level,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
	LastTestAt     string `json:"last_test_at,omitempty"`
	LastTestStatus string `json:"last_test_status,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

// deliveryRecord is one line of the delivery ledger. The message body is never
// recorded: it carries a run summary that may describe private context.
type deliveryRecord struct {
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Channel string `json:"channel"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Title   string `json:"title,omitempty"`
	RunID   string `json:"run_id,omitempty"`
}

// ChannelState returns the masked channel configuration plus recent delivery
// attempts. The device key is never included.
func (s *Store) ChannelState() (map[string]any, error) {
	settings, err := s.loadChannelSettings()
	if err != nil {
		return nil, err
	}
	return s.channelStatePayload(settings), nil
}

// SaveBarkChannel validates and persists the Bark configuration. An empty
// device key keeps the stored one, so the UI never needs to round-trip it.
func (s *Store) SaveBarkChannel(payload map[string]any) (map[string]any, error) {
	enabled, _ := payload["enabled"].(bool)
	server := channelString(payload["server"], 2048)
	deviceKey := channelString(payload["device_key"], 512)
	group := channelString(payload["group"], 64)
	sound := channelString(payload["sound"], 64)
	level := channelString(payload["level"], 32)
	language := channelString(payload["language"], 8)

	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	settings, err := s.loadChannelSettingsLocked()
	if err != nil {
		return nil, err
	}
	if language != "" {
		normalized, ok := normalizeLanguage(language)
		if !ok {
			return nil, errStore("invalid_language")
		}
		settings.Language = normalized
	}
	candidate := notify.BarkConfig{
		Server:    firstNonEmptyString(server, settings.Bark.Server),
		DeviceKey: firstNonEmptyString(deviceKey, settings.Bark.DeviceKey),
		Group:     firstNonEmptyString(group, settings.Bark.Group),
		Sound:     firstNonEmptyString(sound, settings.Bark.Sound),
		Level:     firstNonEmptyString(level, settings.Bark.Level),
	}
	normalized, err := notify.ValidateBark(candidate)
	if err != nil {
		return nil, errStore("invalid_channel")
	}
	if enabled && normalized.DeviceKey == "" {
		return nil, errStore("device_key_required")
	}
	settings.Bark = barkSettings{
		Enabled:        enabled,
		Server:         normalized.Server,
		DeviceKey:      normalized.DeviceKey,
		Group:          normalized.Group,
		Sound:          normalized.Sound,
		Level:          normalized.Level,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		LastTestAt:     settings.Bark.LastTestAt,
		LastTestStatus: settings.Bark.LastTestStatus,
		LastError:      settings.Bark.LastError,
	}
	if err := s.saveChannelSettingsLocked(settings); err != nil {
		return nil, err
	}
	return s.channelStatePayload(settings), nil
}

// TestBarkChannel sends a fixed message with the stored configuration. A
// delivery failure is reported in the receipt, not as an error: the
// configuration itself may still be valid.
func (s *Store) TestBarkChannel(override map[string]any) (map[string]any, error) {
	settings, err := s.loadChannelSettings()
	if err != nil {
		return nil, err
	}
	// Testing the values on screen lets the owner verify a key before saving
	// it. A blank override falls back to the stored configuration.
	server := firstNonEmptyString(channelString(override["server"], 2048), settings.Bark.Server)
	deviceKey := firstNonEmptyString(channelString(override["device_key"], 512), settings.Bark.DeviceKey)
	if deviceKey == "" {
		return nil, errStore("channel_not_configured")
	}
	channel, err := notify.NewBark(notify.BarkConfig{
		Server:    server,
		DeviceKey: deviceKey,
		Group:     settings.Bark.Group,
		Sound:     settings.Bark.Sound,
		Level:     settings.Bark.Level,
	})
	if err != nil {
		return nil, errStore("invalid_channel")
	}
	message := testNotification(settings.Language)
	ctx, cancel := context.WithTimeout(context.Background(), testNotifyTimeout)
	defer cancel()
	receipt := channel.Send(ctx, message)
	s.recordDelivery("test", receipt, "", message.Title)
	updated, err := s.updateTestResult(receipt)
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

func (s *Store) channelStatePayload(settings channelSettings) map[string]any {
	level := settings.Bark.Level
	if level == "" {
		level = string(notify.LevelActive)
	}
	return map[string]any{
		"language": languageOrDefault(settings.Language),
		"bark": map[string]any{
			"enabled":          settings.Bark.Enabled,
			"configured":       strings.TrimSpace(settings.Bark.DeviceKey) != "",
			"server":           settings.Bark.Server,
			"group":            settings.Bark.Group,
			"sound":            settings.Bark.Sound,
			"level":            level,
			"updated_at":       settings.Bark.UpdatedAt,
			"last_test_at":     settings.Bark.LastTestAt,
			"last_test_status": settings.Bark.LastTestStatus,
			"last_error":       settings.Bark.LastError,
		},
		"deliveries": s.recentDeliveries(maxDeliveryList),
	}
}

func (s *Store) loadChannelSettings() (channelSettings, error) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	return s.loadChannelSettingsLocked()
}

func (s *Store) loadChannelSettingsLocked() (channelSettings, error) {
	if s.channelSettings != nil {
		return *s.channelSettings, nil
	}
	info, err := os.Lstat(s.channelsPath)
	if err != nil {
		if os.IsNotExist(err) {
			empty := channelSettings{}
			s.channelSettings = &empty
			return empty, nil
		}
		return channelSettings{}, errStore("channel settings are unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxChannelConfigBytes {
		return channelSettings{}, errStore("channel settings are invalid")
	}
	raw, err := os.ReadFile(s.channelsPath)
	if err != nil || int64(len(raw)) > maxChannelConfigBytes {
		return channelSettings{}, errStore("channel settings are unavailable")
	}
	var settings channelSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return channelSettings{}, errStore("channel settings are invalid")
	}
	s.channelSettings = &settings
	return settings, nil
}

func (s *Store) saveChannelSettingsLocked(settings channelSettings) error {
	if err := ensurePrivateDir(filepath.Dir(s.channelsPath)); err != nil {
		return err
	}
	if err := s.savePrivateJSON(s.channelsPath, settings); err != nil {
		return err
	}
	s.channelSettings = &settings
	return nil
}

func (s *Store) updateTestResult(receipt notify.Receipt) (channelSettings, error) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	settings, err := s.loadChannelSettingsLocked()
	if err != nil {
		return channelSettings{}, err
	}
	settings.Bark.LastTestAt = receipt.At
	settings.Bark.LastTestStatus = receipt.Status
	if receipt.Status == notify.StatusDelivered {
		settings.Bark.LastError = ""
	} else {
		settings.Bark.LastError = receipt.Detail
	}
	if err := s.saveChannelSettingsLocked(settings); err != nil {
		return channelSettings{}, err
	}
	return settings, nil
}

// notifyRun enqueues one push for a finished automatic run. It never blocks
// the run and never changes its outcome: a notification is best effort.
func (s *Store) notifyRun(run map[string]any) {
	status := publicText(run["status"], 32)
	if status != "pushed" && status != "failed" {
		return
	}
	settings, err := s.loadChannelSettings()
	if err != nil || !settings.Bark.Enabled || strings.TrimSpace(settings.Bark.DeviceKey) == "" {
		return
	}
	kind := "run:" + status
	if !s.allowNotification(kind) {
		return
	}
	message := runNotification(settings.Language, run)
	runID := publicText(run["run_id"], 64)
	config := notify.BarkConfig{
		Server:    settings.Bark.Server,
		DeviceKey: settings.Bark.DeviceKey,
		Group:     settings.Bark.Group,
		Sound:     settings.Bark.Sound,
		Level:     settings.Bark.Level,
	}
	go func() {
		// The run lock is held while this is scheduled, so the network call
		// happens strictly after the caller releases it.
		channel, err := notify.NewBark(config)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), pushNotifyTimeout)
		defer cancel()
		receipt := channel.Send(ctx, message)
		s.recordDelivery(kind, receipt, runID, message.Title)
	}()
}

// allowNotification limits how often one kind of push may fire, so a burst of
// failing runs cannot flood the device.
func (s *Store) allowNotification(kind string) bool {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	if s.notifyLast == nil {
		s.notifyLast = map[string]time.Time{}
	}
	now := time.Now()
	if last, ok := s.notifyLast[kind]; ok && now.Sub(last) < notifyThrottle {
		return false
	}
	s.notifyLast[kind] = now
	return true
}

func (s *Store) recordDelivery(kind string, receipt notify.Receipt, runID, title string) {
	if receipt.Channel == "" {
		return
	}
	record := deliveryRecord{
		At:      receipt.At,
		Kind:    notify.ClampRunes(kind, 64),
		Channel: receipt.Channel,
		Status:  receipt.Status,
		Detail:  receipt.Detail,
		Title:   notify.ClampRunes(title, notify.MaxTitleRunes),
		RunID:   notify.ClampRunes(runID, 64),
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if ensurePrivateDir(s.runtimeDir) != nil {
		return
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return
	}
	file, err := os.OpenFile(s.deliveriesPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, writeErr := file.Write(append(raw, '\n'))
	_ = file.Close()
	if writeErr == nil {
		s.pruneDeliveriesLocked()
	}
}

// pruneDeliveriesLocked keeps the ledger bounded by rewriting it when it grows
// past the size cap. Losing old deliveries is acceptable; they are evidence,
// not authority.
func (s *Store) pruneDeliveriesLocked() {
	info, err := os.Lstat(s.deliveriesPath)
	if err != nil || info.Size() <= maxDeliveryLogBytes {
		return
	}
	raw, err := os.ReadFile(s.deliveriesPath)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > maxDeliveryRecords {
		lines = lines[len(lines)-maxDeliveryRecords:]
	}
	trimmed := strings.Join(lines, "\n") + "\n"
	_ = writeAtomic(s.deliveriesPath, []byte(trimmed), 0o600)
}

func (s *Store) recentDeliveries(limit int) []any {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	raw := s.readDeliveryTailLocked()
	if len(raw) == 0 {
		return []any{}
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	result := make([]any, 0, limit)
	for index := len(lines) - 1; index >= 0 && len(result) < limit; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			continue
		}
		var record deliveryRecord
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		result = append(result, map[string]any{
			"at":      record.At,
			"kind":    record.Kind,
			"channel": record.Channel,
			"status":  record.Status,
			"detail":  record.Detail,
			"title":   record.Title,
			"run_id":  record.RunID,
		})
	}
	return result
}

func (s *Store) readDeliveryTailLocked() []byte {
	info, err := os.Lstat(s.deliveriesPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil
	}
	if info.Size() <= maxDeliveryLogBytes {
		raw, err := os.ReadFile(s.deliveriesPath)
		if err != nil {
			return nil
		}
		return raw
	}
	file, err := os.Open(s.deliveriesPath)
	if err != nil {
		return nil
	}
	defer file.Close()
	if _, err := file.Seek(info.Size()-maxDeliveryLogBytes, io.SeekStart); err != nil {
		return nil
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil
	}
	return raw
}

func testNotification(language string) notify.Message {
	if languageOrDefault(language) == "zh" {
		// No explicit level: the channel's configured urgency applies.
		return notify.Message{
			Title: "Folio 测试通知",
			Body:  "Bark 接入已生效。维护完成或失败时会推送到这里。",
		}
	}
	return notify.Message{
		Title: "Folio test notification",
		Body:  "Bark delivery works. Maintenance results will arrive here.",
	}
}

// runNotification renders the bounded push text for a finished run.
func runNotification(language string, run map[string]any) notify.Message {
	zh := languageOrDefault(language) == "zh"
	runID := publicText(run["run_id"], 64)
	status := publicText(run["status"], 32)
	edits := 0
	if list, ok := run["edits"].([]any); ok {
		edits = len(list)
	}
	summary := notify.ClampRunes(strings.TrimSpace(publicText(run["summary"], 600)), 600)
	if status == "pushed" {
		if zh {
			body := fmt.Sprintf("已推送 %d 处修改。", edits)
			if summary != "" {
				body += "\n" + summary
			}
			return notify.Message{Title: "Folio · 上下文已更新", Body: withRun(body, runID)}
		}
		body := fmt.Sprintf("Pushed %d change(s).", edits)
		if summary != "" {
			body += "\n" + summary
		}
		return notify.Message{Title: "Folio · Context updated", Body: withRun(body, runID)}
	}
	stage := publicText(run["stage"], 32)
	reason := publicText(run["reason"], 64)
	if reason == "" {
		reason = "failed"
	}
	if zh {
		return notify.Message{
			Title: "Folio · 维护失败",
			Body:  withRun(fmt.Sprintf("阶段 %s：%s", stage, reason), runID),
		}
	}
	return notify.Message{
		Title: "Folio · Maintenance failed",
		Body:  withRun(fmt.Sprintf("Stage %s: %s", stage, reason), runID),
	}
}

func withRun(body, runID string) string {
	if runID == "" {
		return body
	}
	return body + "\nrun " + runID
}

func normalizeLanguage(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "zh":
		return "zh", true
	case "en":
		return "en", true
	default:
		return "", false
	}
}

func languageOrDefault(raw string) string {
	if normalized, ok := normalizeLanguage(raw); ok {
		return normalized
	}
	return "en"
}

func channelString(value any, limit int) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return notify.ClampRunes(strings.TrimSpace(text), limit)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

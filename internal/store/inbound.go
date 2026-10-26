package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"folio/internal/channel"
)

// WeixinHandler is the application authorization boundary. The channel carries
// identity; this adapter grants only an explicitly configured owner access to
// existing Folio behavior. There is no first-message pairing or prompt-based
// authorization. Non-owner personas must get a separate restricted handler.
func (s *Store) WeixinHandler(accountID, ownerID string) (channel.Handler, error) {
	if !validChannelIdentity(accountID) || !validChannelIdentity(ownerID) {
		return nil, errStore("weixin account and explicit owner are required")
	}
	return &weixinHandler{store: s, accountID: accountID, ownerID: ownerID}, nil
}

type weixinHandler struct {
	store              *Store
	accountID, ownerID string
}

func (h *weixinHandler) Authorize(m channel.Message) error {
	if m.Channel != "weixin" || m.AccountID != h.accountID || m.SenderID != h.ownerID || m.ConversationID != h.ownerID {
		return channel.ErrUnauthorized
	}
	return nil
}

func (h *weixinHandler) HandleMessage(ctx context.Context, m channel.Message) (string, error) {
	if err := h.Authorize(m); err != nil {
		return "", err
	}
	if m.ID == "" || len(m.Text) > 16*1024 || strings.TrimSpace(m.Text) == "" || len(m.Attachments) != 0 {
		return "", errStore("unsupported channel message")
	}
	switch strings.TrimSpace(m.Text) {
	case "/help":
		return "Folio：发送文本可获得只读回答。/status 查看服务状态。本入口暂不读取私有 Context，不执行维护、工具或仓库写入。", nil
	case "/status":
		state, err := h.store.State()
		if err != nil {
			return "", err
		}
		connected, _ := mapValue(state, "repository")["connected"].(bool)
		if connected {
			return "Folio 在线，Context 仓库已连接。微信入口仅支持只读回答。", nil
		}
		return "Folio 在线，Context 仓库尚未连接。微信入口仅支持只读回答。", nil
	default:
		// Each message is a fresh read-only report invocation. Never resume
		// a shared CLI conversation using a transport-supplied session ID.
		return h.store.channelReport(ctx, m.Text)
	}
}

func validChannelIdentity(s string) bool {
	if s == "" || len(s) > 512 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *Store) channelReport(ctx context.Context, text string) (string, error) {
	for !s.modelMu.TryLock() {
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	defer s.modelMu.Unlock()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	work, err := os.MkdirTemp(s.runtimeDir, "channel-report-")
	if err != nil {
		return "", errStore("channel report unavailable")
	}
	defer os.RemoveAll(work)
	promptPath := filepath.Join(work, "prompt.txt")
	outputPath := filepath.Join(work, "result.json")
	// No channel credentials, peer IDs or external-user context reach the model.
	prompt := "Answer the following owner message as Folio, concisely in the user's language. This is a read-only text answer; no private context has been supplied.\n\n" + text
	if os.WriteFile(promptPath, []byte(prompt), 0o600) != nil {
		return "", errStore("channel report unavailable")
	}
	command, err := s.installedFolioCommand()
	if err != nil {
		return "", errStore("channel report unavailable")
	}
	command = append(command, "agent", "--report-prompt", promptPath, "--report-out", outputPath,
		"--runtime-dir", s.runtimeDir, "--runner", "codex", "--timeout", "180")
	env := map[string]string{
		"FOLIO_CONTEXT_ROOT": s.repositoryDir, "FOLIO_RUNTIME": s.runtimeDir,
		"CODEX_HOME": s.codexDir, "DSH_HOME": s.dshDir,
	}
	if _, err := s.runCommandContext(ctx, command, s.toolRoot, "", 210*time.Second, true, env); err != nil {
		return "", errStore("channel report failed")
	}
	info, err := os.Lstat(outputPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxReportOutputBytes {
		return "", errStore("channel report invalid")
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil || len(raw) > maxReportOutputBytes {
		return "", errStore("channel report invalid")
	}
	var result reportResult
	if json.Unmarshal(raw, &result) != nil || strings.TrimSpace(result.Text) == "" || len(result.Text) > maxReportTextBytes || secretMaterialRE.MatchString(result.Text) {
		return "", errStore("channel report rejected")
	}
	return strings.TrimSpace(result.Text), nil
}

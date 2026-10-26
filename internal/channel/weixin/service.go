package weixin

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"folio/internal/channel"
)

type Service struct {
	disk        *Disk
	client      *Client
	state       *syncState
	handler     channel.Handler
	pollTimeout time.Duration
}

func NewService(disk *Disk, handler channel.Handler) (*Service, error) {
	creds, err := disk.Credentials()
	if err != nil {
		return nil, err
	}
	c, err := NewClient(creds)
	if err != nil {
		return nil, err
	}
	state, err := disk.state(creds.AccountID)
	if err != nil || handler == nil {
		return nil, ErrState
	}
	return &Service{disk: disk, client: c, state: state, handler: handler, pollTimeout: 40 * time.Second}, nil
}

// Run serializes the small text MVP. Cursor movement and durable acceptance
// happen together; pending work is always drained before requesting more.
func (s *Service) Run(ctx context.Context) error {
	// Lifecycle hints are advisory in the reference implementation too.
	_ = s.client.request(ctx, "msg/notifystart", map[string]any{}, true, 5*time.Second, &struct{}{})
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.client.request(stopCtx, "msg/notifystop", map[string]any{}, true, 2*time.Second, &struct{}{})
	}()
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		err := s.step(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrState) || errors.Is(err, ErrProtocol) {
			return err
		}
		if err == nil {
			backoff = 2 * time.Second
			// Bound tight polling if a server returns empty batches immediately.
			if err := delay(ctx, 200*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if err := delay(ctx, backoff); err != nil {
			return err
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
	return ctx.Err()
}

func (s *Service) step(ctx context.Context) error {
	if len(s.state.Pending) > 0 {
		return s.drain(ctx)
	}
	batch, err := s.client.getUpdates(ctx, s.state.Cursor, s.pollTimeout)
	if err != nil {
		return err
	}
	if len(batch.Messages) > 100 || len(batch.Cursor) > 256*1024 {
		return ErrProtocol
	}
	seen := make(map[string]bool, len(s.state.Recent)+len(batch.Messages))
	for _, key := range s.state.Recent {
		seen[key] = true
	}
	for _, raw := range batch.Messages {
		message, ok := incoming(s.state.AccountID, raw)
		if !ok || seen[message.Key()] {
			continue
		}
		seen[message.Key()] = true
		s.state.Pending = append(s.state.Pending, delivery{Message: message, ContextToken: raw.ContextToken})
	}
	if batch.Cursor != "" {
		s.state.Cursor = batch.Cursor
	}
	// Save even an empty batch: skipped unsupported events are acknowledged.
	if err := s.disk.save("sync.json", s.state); err != nil {
		return err
	}
	if batch.TimeoutMS >= 1000 && batch.TimeoutMS <= 60000 {
		s.pollTimeout = time.Duration(batch.TimeoutMS)*time.Millisecond + 5*time.Second
	}
	return s.drain(ctx)
}

func incoming(account string, raw wireMessage) (channel.Message, bool) {
	// Group identity/routing and mixed media are deliberately unsupported. No
	// content can spoof a sender or promote an external peer to the owner.
	if raw.Type != 1 || raw.State != 2 || raw.Group != "" || !validOpaque(raw.From, 512) || !validOpaque(string(raw.ID), 512) || !validOpaque(raw.ContextToken, 8192) || len(raw.Session) > 512 || len(raw.Items) == 0 || len(raw.Items) > 32 {
		return channel.Message{}, false
	}
	if raw.To != "" && raw.To != account {
		return channel.Message{}, false
	}
	parts := make([]string, 0, len(raw.Items))
	for _, it := range raw.Items {
		if it.Type != 1 || it.Text == nil {
			return channel.Message{}, false
		}
		parts = append(parts, it.Text.Text)
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" || len(text) > maxTextBytes || !utf8.ValidString(text) {
		return channel.Message{}, false
	}
	return channel.Message{Channel: "weixin", AccountID: account, ID: string(raw.ID), SenderID: raw.From, ConversationID: raw.From, SessionID: raw.Session, Text: text}, true
}

func (s *Service) drain(ctx context.Context) error {
	for len(s.state.Pending) > 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p := &s.state.Pending[0]
		if err := s.handler.Authorize(p.Message); err != nil {
			if !errors.Is(err, channel.ErrUnauthorized) {
				return err
			}
			p.Ready, p.Reply = true, ""
		}
		if !p.Ready {
			reply, err := s.handler.HandleMessage(ctx, p.Message)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			switch {
			case errors.Is(err, channel.ErrUnauthorized):
				reply = "" // Refuse silently, before any model or privileged call.
			case err != nil:
				reply = "Folio 暂时无法处理这条消息，请稍后重试。"
			}
			if len(reply) > maxTextBytes || utf8.RuneCountInString(reply) > 4000 || !utf8.ValidString(reply) {
				reply = "Folio 的回复超过文本长度限制，请缩小问题范围。"
			}
			p.Reply, p.Ready = reply, true
			if err := s.disk.save("sync.json", s.state); err != nil {
				return err
			}
		}
		if p.Reply != "" {
			// The protocol does not promise idempotence. A stable client ID is
			// reused across retries, including a restart after an uncertain send.
			if err := s.client.sendText(ctx, p.Message.SenderID, p.ContextToken, "folio-weixin-"+p.Message.Key(), p.Reply); err != nil {
				return err
			}
		}
		s.state.Recent = append(s.state.Recent, p.Message.Key())
		if len(s.state.Recent) > maxRecent {
			s.state.Recent = s.state.Recent[len(s.state.Recent)-maxRecent:]
		}
		s.state.Pending = s.state.Pending[1:]
		if err := s.disk.save("sync.json", s.state); err != nil {
			return err
		}
	}
	return nil
}

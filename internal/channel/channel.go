// Package channel defines the small communication boundary into Folio.
package channel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

var ErrUnauthorized = errors.New("channel sender is not authorized")

// Message contains identity supplied by the authenticated transport, never by
// message text. AccountID namespaces sender IDs to one channel account.
// ConversationID is the direct peer in the Weixin MVP; SessionID is opaque
// transport metadata, not authority or a model-runner resume identifier.
type Message struct {
	Channel        string       `json:"channel"`
	AccountID      string       `json:"account_id"`
	ID             string       `json:"id"`
	SenderID       string       `json:"sender_id"`
	ConversationID string       `json:"conversation_id"`
	SessionID      string       `json:"session_id,omitempty"`
	Text           string       `json:"text"`
	Attachments    []Attachment `json:"attachments,omitempty"`
}

// Attachment reserves a boundary for later media support; no downloads or
// filesystem paths are accepted by the text MVP.
type Attachment struct {
	Kind string `json:"kind"`
}

// Key scopes durable deduplication to channel, account, peer and message.
func (m Message) Key() string {
	raw, _ := json.Marshal([]string{m.Channel, m.AccountID, m.SenderID, m.ConversationID, m.ID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// SessionKey cannot collide across users even if a backend reuses session IDs.
func (m Message) SessionKey() string {
	raw, _ := json.Marshal([]string{m.Channel, m.AccountID, m.SenderID, m.ConversationID, m.SessionID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type Handler interface {
	// Authorize is checked even for a saved reply, so revoking access also
	// prevents delivery of an answer prepared before a restart.
	Authorize(Message) error
	HandleMessage(context.Context, Message) (string, error)
}

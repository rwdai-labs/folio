package weixin

import (
	"sort"
	"time"
)

// contacts keeps the newest reply context token per peer. The protocol sends a
// proactive message only inside a context that an inbound message created, so a
// push to an owner who never wrote, or whose context has expired, cannot be
// delivered. Tokens never leave this file and are never shown in the console.
type contacts struct {
	Version int                  `json:"version"`
	Peers   map[string]peerToken `json:"peers"`
}

type peerToken struct {
	Token string `json:"context_token"`
	At    string `json:"at"`
}

const (
	contactsVersion = 1
	contactsFile    = "contacts.json"
	maxContacts     = 8
)

func emptyContacts() contacts {
	return contacts{Version: contactsVersion, Peers: map[string]peerToken{}}
}

// loadContacts ignores a missing, corrupt or future file: a stale token only
// fails the push it was meant for, never the channel.
func loadContacts(disk *Disk) contacts {
	value := emptyContacts()
	if disk == nil {
		return value
	}
	var stored contacts
	if err := disk.read(contactsFile, &stored); err != nil || stored.Version != contactsVersion {
		return value
	}
	for user, peer := range stored.Peers {
		if !validOpaque(user, 512) || !validOpaque(peer.Token, 8192) {
			continue
		}
		value.Peers[user] = peer
	}
	return value
}

// rememberContact stores the reply context of one peer that actually received a
// reply, so a sender that was refused never occupies the bounded table.
func rememberContact(disk *Disk, user, token string) error {
	if !validOpaque(user, 512) || !validOpaque(token, 8192) {
		return ErrProtocol
	}
	value := loadContacts(disk)
	value.Peers[user] = peerToken{Token: token, At: time.Now().UTC().Format(time.RFC3339)}
	if len(value.Peers) > maxContacts {
		users := make([]string, 0, len(value.Peers))
		for name := range value.Peers {
			users = append(users, name)
		}
		sort.Slice(users, func(i, j int) bool { return value.Peers[users[i]].At < value.Peers[users[j]].At })
		for _, name := range users[:len(value.Peers)-maxContacts] {
			delete(value.Peers, name)
		}
	}
	return disk.save(contactsFile, value)
}

// contactFor returns the stored context token of one peer.
func contactFor(disk *Disk, user string) (peerToken, bool) {
	peer, ok := loadContacts(disk).Peers[user]
	return peer, ok
}

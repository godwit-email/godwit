package ews

import (
	"context"
	"errors"

	"github.com/godwit-email/godwit/backend"
)

// Sender is an EWS account's sending backend.
// shortcut: sends nothing yet, failing each try before the server sees the
// message (so the outbox keeps it and tries again); sending with CreateItem
// SendAndSaveCopy replaces it.
type Sender struct{}

var _ backend.Sender = Sender{}

var errNoSend = errors.New("ews: sending through EWS is not available yet")

// Send implements backend.Sender.
func (Sender) Send(context.Context, string, []string, []byte) error { return errNoSend }

// SavesSent implements backend.Sender: Exchange files what it sends in Sent Items.
func (Sender) SavesSent() bool { return true }

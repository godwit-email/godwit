package ews

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// GetStreamingEvents: a hanging request on a streaming subscription. The
// server writes one SOAP envelope per notification into the open response
// until ConnectionTimeout, then a last one with ConnectionStatus Closed.

type getStreamingEventsRequest struct {
	XMLName         xml.Name `xml:"m:GetStreamingEvents"`
	SubscriptionIDs []string `xml:"m:SubscriptionIds>t:SubscriptionId"`
	Timeout         int      `xml:"m:ConnectionTimeout"` // minutes, 1 to 30
}

type streamingMessage struct {
	responseMessage
	Notifications []struct {
		Events []struct { // NewMailEvent, ModifiedEvent, ...; also the SubscriptionId
			Parent    *id `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
			OldParent *id `xml:"http://schemas.microsoft.com/exchange/services/2006/types OldParentFolderId"`
		} `xml:",any"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Notifications>Notification"`
	Status string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ConnectionStatus"` // OK or Closed
}

// streamTimeout is how long one connection lasts: the most the server allows.
const streamTimeout = 30 * time.Minute

// streamIdle is how long a connection may stay silent before it is opened
// again, so one that died without a word is noticed.
// shortcut: Exchange's heartbeat interval on a stream is not verified; if it
// sends none, this only reopens an idle connection every few minutes.
const streamIdle = 3 * time.Minute

var errIdle = errors.New("ews: the streaming connection went silent")

// stream opens one GetStreamingEvents connection on the subscription and
// calls wake with the folders of each notification's events. It returns nil
// once the connection is over and may be opened again (the server closed it,
// or it went silent), a *ResponseError when the server refuses the
// subscription, any other error when the connection failed.
// It goes outside call: the account's other requests go on meanwhile.
func (c *client) stream(ctx context.Context, subscription string, wake func([]backend.FolderRef)) error {
	cr, err := c.cfg.Credentials(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idle := time.AfterFunc(streamIdle, func() { cancel(errIdle) })
	defer idle.Stop()
	resp, body, err := c.post(ctx, getStreamingEventsRequest{SubscriptionIDs: []string{subscription}, Timeout: int(streamTimeout / time.Minute)}, cr)
	if err != nil {
		return silent(ctx, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return c.refused(ctx, resp, body, cr)
	default:
		return fmt.Errorf("ews: GetStreamingEvents: HTTP %s", resp.Status)
	}
	d := xml.NewDecoder(resp.Body)
	for {
		tok, err := d.Token()
		if err != nil {
			return silent(ctx, fmt.Errorf("ews: GetStreamingEvents: %w", err))
		}
		el, ok := tok.(xml.StartElement)
		switch {
		case !ok:
			continue
		case el.Name.Space == nsSOAP && el.Name.Local == "Fault":
			f := new(fault)
			if err := d.DecodeElement(f, &el); err != nil {
				return silent(ctx, err)
			}
			return f
		case el.Name.Space != nsMessages || el.Name.Local != "GetStreamingEventsResponseMessage":
			continue
		}
		var m streamingMessage
		if err := d.DecodeElement(&m, &el); err != nil {
			return silent(ctx, err)
		}
		idle.Reset(streamIdle)
		if err := m.err(); err != nil {
			return err
		}
		var folders []backend.FolderRef
		for _, n := range m.Notifications {
			for _, e := range n.Events {
				for _, p := range []*id{e.Parent, e.OldParent} {
					if p != nil && !slices.Contains(folders, backend.FolderRef(p.ID)) {
						folders = append(folders, backend.FolderRef(p.ID))
					}
				}
			}
		}
		if len(folders) > 0 {
			wake(folders)
		}
		if m.Status == "Closed" {
			return nil
		}
	}
}

// silent is err, or nil when it came of the connection going silent.
func silent(ctx context.Context, err error) error {
	if context.Cause(ctx) == errIdle {
		return nil
	}
	return err
}

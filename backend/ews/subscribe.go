package ews

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"sync"
)

// Subscribe: a streaming subscription to every folder's mail events.

type subscribeRequest struct {
	XMLName   xml.Name `xml:"m:Subscribe"`
	Streaming struct {
		AllFolders bool     `xml:"SubscribeToAllFolders,attr"`
		EventTypes []string `xml:"t:EventTypes>t:EventType"`
	} `xml:"m:StreamingSubscriptionRequest"`
}

type subscribeResponse struct {
	Messages []subscribeMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages>SubscribeResponseMessage"`
}

type subscribeMessage struct {
	responseMessage
	SubscriptionID string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscriptionId"`
}

// subscribe makes a streaming subscription to all folders and returns its id.
func (c *client) subscribe(ctx context.Context) (string, error) {
	var req subscribeRequest
	req.Streaming.AllFolders = true
	req.Streaming.EventTypes = []string{"NewMailEvent", "CreatedEvent", "DeletedEvent", "ModifiedEvent", "MovedEvent", "CopiedEvent"}
	resp, err := call[subscribeResponse](ctx, c, req)
	if err != nil {
		return "", err
	}
	if len(resp.Messages) != 1 {
		return "", fmt.Errorf("ews: Subscribe: %d responses", len(resp.Messages))
	}
	if err := resp.Messages[0].err(); err != nil {
		return "", err
	}
	return resp.Messages[0].SubscriptionID, nil
}

// Subscription affinity (spec 多台 CAS): with several Client Access servers
// in front, a subscription lives on one mailbox server, and requests for it
// reach that server only by the X-BackEndOverrideCookie the first Subscribe
// sets. Every request of the account carries it once known.

// affinity is an account's backend cookie and its streaming subscription,
// shared by every client of the account, as a Receiver lasts one sync round
// and the subscription outlives it.
type affinity struct {
	mu           sync.Mutex
	cookie       string // X-BackEndOverrideCookie, "" before the first Subscribe
	subscription string // the streaming subscription's id, "" for none
}

var affinities sync.Map // account key → *affinity

func (c *client) affinity(account string) *affinity {
	v, _ := affinities.LoadOrStore(account, &affinity{})
	return v.(*affinity)
}

const backEndCookie = "X-BackEndOverrideCookie"

// route adds the affinity headers to req, a request of op: Subscribe without
// a cookie anchors on the account's mailbox; any request with one carries it.
func (a *affinity) route(req *http.Request, op any, anchor string) {
	a.mu.Lock()
	cookie := a.cookie
	a.mu.Unlock()
	if _, ok := op.(subscribeRequest); ok && cookie == "" && anchor != "" {
		req.Header.Set("X-AnchorMailbox", anchor)
		req.Header.Set("X-PreferServerAffinity", "true")
	}
	if cookie != "" {
		req.Header.Set("X-PreferServerAffinity", "true")
		req.AddCookie(&http.Cookie{Name: backEndCookie, Value: cookie})
	}
}

// keep stores the backend cookie resp sets; only the first Subscribe's does.
func (a *affinity) keep(resp *http.Response) {
	for _, ck := range resp.Cookies() {
		if ck.Name == backEndCookie && ck.Value != "" {
			a.mu.Lock()
			a.cookie = ck.Value
			a.mu.Unlock()
		}
	}
}

// lost forgets the subscription id and the cookie that routed to it, so the
// next Subscribe anchors afresh.
func (a *affinity) lost(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.subscription == id {
		a.subscription, a.cookie = "", ""
	}
}

package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/mail"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	gojmap "git.sr.ht/~rockorager/go-jmap"
	jmail "git.sr.ht/~rockorager/go-jmap/mail"
	"git.sr.ht/~rockorager/go-jmap/mail/email"
	"git.sr.ht/~rockorager/go-jmap/mail/emailsubmission"
	"git.sr.ht/~rockorager/go-jmap/mail/identity"
	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"

	"github.com/godwit-email/godwit/backend"
)

// sendTimeout bounds one send so a hung server cannot hold the outbox forever.
// Timing out once the EmailSubmission/set request is written is ErrUncertain.
const sendTimeout = 5 * time.Minute

// Sender is a JMAP account's sending backend (RFC 8621 section 7): it uploads
// the RFC 5322 message as a blob, then imports the Email and creates an
// EmailSubmission with an explicit envelope and onSuccessUpdateEmail in one
// JMAP request.
type Sender struct {
	c       *client
	unsaved atomic.Bool // last accepted Send could not store the sent copy in Sent
}

var _ backend.Sender = (*Sender)(nil)

// NewSender is a Sender for the account cfg names.
func NewSender(cfg Config) *Sender { return &Sender{c: newClient(cfg)} }

// SavesSent implements backend.Sender: Email/import + onSuccessUpdateEmail
// places the sent copy in the Sent mailbox on the server, unless the follow-up
// Email/set failed (or the configured Sent mailbox was missing), in which case
// SavesSent reports false so core's outbox archives or retains recovery state.
func (s *Sender) SavesSent() bool { return !s.unsaved.Load() }

// Send implements backend.Sender. Any failure before the EmailSubmission/set
// HTTP request is written whole (session, Identity/get, blob upload) is a
// plain error (or ErrAuthFailed / *RejectedError) and safe to retry. Once the
// submission HTTP request has been written, a lost response (timeout, dropped
// connection, unreadable body, or HTTP 5xx) returns backend.ErrUncertain.
func (s *Sender) Send(ctx context.Context, from string, rcpts []string, raw []byte) error {
	s.unsaved.Store(false)
	defer s.c.http.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	sess, acct, err := s.c.sess(ctx)
	if err != nil {
		return err
	}
	if !hasSubmission(sess, acct) {
		return fmt.Errorf("%w: %w", ErrNoSubmission, &backend.RejectedError{
			Stage:    "MAIL",
			Code:     550,
			Enhanced: "5.7.1",
			Message:  ReasonNoSubmission, // the outbox shows its localized reason: get a token that may send
		})
	}

	if from == "" {
		from = headerFrom(raw)
	}

	// Fetch mailboxes and identities before uploading or submitting.
	meta, err := s.c.call(ctx, func(acct gojmap.ID) []gojmap.Method {
		return []gojmap.Method{
			&mailbox.Get{Account: acct},
			&identity.Get{Account: acct},
		}
	})
	if err != nil {
		return err
	}
	boxes := meta[0].(*mailbox.GetResponse).List
	identities := meta[1].(*identity.GetResponse).List

	idID, ok := matchIdentity(identities, from)
	if !ok {
		return &backend.RejectedError{
			Stage:    "MAIL",
			Code:     550,
			Enhanced: "5.7.1",
			Message:  fmt.Sprintf("no JMAP identity allows sending from %s", from),
		}
	}

	stageMailbox, onSuccessUpdate, onSuccessDestroy, missingSent, err := planStaging(boxes, s.c.cfg.SentFolder)
	if err != nil {
		return err
	}

	blobID, err := s.c.upload(ctx, raw)
	if err != nil {
		return err
	}

	rcptAddrs := make([]*emailsubmission.Address, 0, len(rcpts))
	for _, r := range rcpts {
		rcptAddrs = append(rcptAddrs, &emailsubmission.Address{Email: r})
	}
	env := &emailsubmission.Envelope{
		MailFrom: &emailsubmission.Address{Email: from},
		RcptTo:   rcptAddrs,
	}

	req := &gojmap.Request{Using: []gojmap.URI{gojmap.CoreURI, jmail.URI, emailsubmission.URI}}
	impCall := req.Invoke(&email.Import{
		Account: acct,
		Emails: map[string]*email.EmailImport{
			"m": {
				BlobID:     blobID,
				MailboxIDs: map[gojmap.ID]bool{stageMailbox: true},
				Keywords:   map[string]bool{"$seen": true, "$draft": true},
			},
		},
	})
	subCall := req.Invoke(&emailsubmission.Set{
		Account: acct,
		Create: map[gojmap.ID]*emailsubmission.EmailSubmission{
			"s": {
				IdentityID: idID,
				EmailID:    "#m",
				Envelope:   env,
			},
		},
		OnSuccessUpdateEmail:  onSuccessUpdate,
		OnSuccessDestroyEmail: onSuccessDestroy,
	})

	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	var wrote atomic.Bool
	subCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn:      func(string) { wrote.Store(false) },
		WroteRequest: func(i httptrace.WroteRequestInfo) { wrote.Store(i.Err == nil) },
	})
	hreq, err := http.NewRequestWithContext(subCtx, http.MethodPost, sess.APIURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")

	hresp, err := s.c.http.Do(hreq)
	if err != nil {
		if wrote.Load() {
			return fmt.Errorf("%w: %v", backend.ErrUncertain, err)
		}
		return fmt.Errorf("jmap send: %w", err)
	}
	respBytes, readErr := io.ReadAll(hresp.Body)
	hresp.Body.Close()

	switch c := hresp.StatusCode; {
	case c == http.StatusUnauthorized:
		return fmt.Errorf("jmap: %s: %w", hreq.URL.Path, backend.ErrAuthFailed)
	case c >= 500:
		return fmt.Errorf("%w: jmap answered HTTP %s", backend.ErrUncertain, hresp.Status)
	case c == http.StatusNotFound:
		return fmt.Errorf("jmap: %s: %w", hreq.URL.Path, backend.ErrNotFound)
	case c != http.StatusOK:
		return fmt.Errorf("jmap: %s: HTTP %s", hreq.URL.Path, hresp.Status)
	}
	if readErr != nil {
		return fmt.Errorf("%w: %v", backend.ErrUncertain, readErr)
	}

	var resp gojmap.Response
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return fmt.Errorf("%w: jmap response: %v", backend.ErrUncertain, err)
	}
	if resp.SessionState != sess.State {
		s.c.mu.Lock()
		s.c.session = nil
		s.c.mu.Unlock()
	}

	var impInv, subInv, postInv *gojmap.Invocation
	for _, inv := range resp.Responses {
		switch {
		case inv.CallID == impCall && impInv == nil:
			impInv = inv
		case inv.CallID == subCall && subInv == nil:
			subInv = inv
		case inv.CallID == subCall && postInv == nil:
			postInv = inv
		}
	}
	if impInv == nil || subInv == nil {
		return fmt.Errorf("%w: incomplete JMAP submission response", backend.ErrUncertain)
	}

	if me, ok := impInv.Args.(*gojmap.MethodError); ok {
		if me.Type == "serverPartialFail" {
			return fmt.Errorf("%w: %v", backend.ErrUncertain, methodError{me})
		}
		return methodError{me}
	}
	ir, ok := impInv.Args.(*email.ImportResponse)
	if !ok {
		return fmt.Errorf("jmap: unexpected Email/import response %T", impInv.Args)
	}
	if se := ir.NotCreated["m"]; se != nil {
		return importError(se)
	}
	created := ir.Created["m"]
	if created == nil || created.ID == "" {
		if sr, ok := subInv.Args.(*emailsubmission.SetResponse); ok && sr.Created["s"] != nil {
			s.unsaved.Store(true)
			return nil
		}
		return errors.New("jmap: Email/import returned neither created nor notCreated")
	}

	cleanupStaged := func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cleanCancel()
		_, _ = s.c.call(cleanCtx, func(acct gojmap.ID) []gojmap.Method {
			return []gojmap.Method{&email.Set{Account: acct, Destroy: []gojmap.ID{created.ID}}}
		})
	}

	if me, ok := subInv.Args.(*gojmap.MethodError); ok {
		// RFC 8620 §3.6.2 allows serverPartialFail after partial state changes:
		// the submission may already have gone out (and may have moved created.ID
		// to Sent), so preserve recovery evidence and report ErrUncertain.
		if me.Type == "serverPartialFail" {
			return fmt.Errorf("%w: %v", backend.ErrUncertain, methodError{me})
		}
		cleanupStaged()
		if me.Type == "forbidden" || me.Type == "accountReadOnly" {
			return &backend.RejectedError{Stage: "MAIL", Code: 550, Enhanced: "5.7.1", Message: me.Error()}
		}
		return methodError{me}
	}
	sr, ok := subInv.Args.(*emailsubmission.SetResponse)
	if !ok {
		return fmt.Errorf("%w: unexpected EmailSubmission/set response %T", backend.ErrUncertain, subInv.Args)
	}
	if se := sr.NotCreated["s"]; se != nil {
		cleanupStaged()
		return submissionRejected(se)
	}
	if sr.Created["s"] != nil {
		switch {
		case len(onSuccessUpdate) > 0 && !archivedOK(postInv, created.ID):
			cleanupStaged()
			s.unsaved.Store(true)
		case len(onSuccessDestroy) > 0 && !destroyedOK(postInv, created.ID):
			cleanupStaged()
		}
		if missingSent {
			s.unsaved.Store(true)
		}
		return nil
	}
	return fmt.Errorf("%w: EmailSubmission/set gave neither created nor notCreated", backend.ErrUncertain)
}

func archivedOK(postInv *gojmap.Invocation, emailID gojmap.ID) bool {
	if postInv == nil {
		return false
	}
	es, ok := postInv.Args.(*email.SetResponse)
	if !ok || es == nil || es.NotUpdated[emailID] != nil {
		return false
	}
	_, updated := es.Updated[emailID]
	return updated
}

func destroyedOK(postInv *gojmap.Invocation, emailID gojmap.ID) bool {
	if postInv == nil {
		return false
	}
	es, ok := postInv.Args.(*email.SetResponse)
	return ok && es != nil && es.NotDestroyed[emailID] == nil && slices.Contains(es.Destroyed, emailID)
}

// matchIdentity selects the best RFC 8621 section 6 Identity for sending from
// addr: exact address match first, then domain wildcard (*@domain), then an
// unrestricted wildcard (*).
func matchIdentity(list []*identity.Identity, addr string) (gojmap.ID, bool) {
	for _, id := range list {
		if id != nil && id.Email != "" && !strings.HasPrefix(id.Email, "*") && strings.EqualFold(id.Email, addr) {
			return id.ID, true
		}
	}
	if _, addrDom, hasAt := strings.Cut(addr, "@"); hasAt && addrDom != "" {
		for _, id := range list {
			if id == nil {
				continue
			}
			if dom, ok := strings.CutPrefix(id.Email, "*@"); ok && dom != "*" && strings.EqualFold(dom, addrDom) {
				return id.ID, true
			}
		}
	}
	for _, id := range list {
		if id != nil && (id.Email == "*" || id.Email == "*@*" || id.Email == "") {
			return id.ID, true
		}
	}
	return "", false
}

// planStaging picks the mailbox for Email/import and the follow-up Email
// update/destroy to run when EmailSubmission/set succeeds. When sentOverride
// is non-nil, *sentOverride overrides the server's RoleSent mailbox (or ""
// disables saving a sent copy); if *sentOverride names a mailbox absent on the
// server, missingSent is reported so the caller retains archival recovery.
func planStaging(boxes []*mailbox.Mailbox, sentOverride *backend.FolderRef) (stage gojmap.ID, update map[gojmap.ID]gojmap.Patch, destroy []gojmap.ID, missingSent bool, err error) {
	hasBox := map[gojmap.ID]bool{}
	var draftsID, detectedSentID, firstID gojmap.ID
	for _, m := range boxes {
		if m == nil || m.ID == "" {
			continue
		}
		hasBox[m.ID] = true
		if firstID == "" {
			firstID = m.ID
		}
		switch m.Role {
		case mailbox.RoleDrafts:
			if draftsID == "" {
				draftsID = m.ID
			}
		case mailbox.RoleSent:
			if detectedSentID == "" {
				detectedSentID = m.ID
			}
		}
	}
	sentID := detectedSentID
	if sentOverride != nil {
		want := gojmap.ID(*sentOverride)
		switch {
		case want == "":
			sentID = ""
		case hasBox[want]:
			sentID = want
		default:
			sentID = ""
			missingSent = true
		}
	}
	switch {
	case draftsID != "" && sentID != "" && draftsID != sentID:
		return draftsID, map[gojmap.ID]gojmap.Patch{
			"#s": {
				"mailboxIds/" + string(draftsID): nil,
				"mailboxIds/" + string(sentID):   true,
				"keywords/$draft":                nil,
			},
		}, nil, false, nil
	case sentID != "":
		return sentID, map[gojmap.ID]gojmap.Patch{
			"#s": {"keywords/$draft": nil},
		}, nil, false, nil
	case draftsID != "":
		return draftsID, nil, []gojmap.ID{"#s"}, missingSent, nil
	case firstID != "":
		return firstID, nil, []gojmap.ID{"#s"}, missingSent, nil
	default:
		return "", nil, nil, false, errors.New("jmap: account has no mailboxes to stage submission")
	}
}

func importError(se *gojmap.SetError) error {
	msg := formatSetError(se)
	switch se.Type {
	case "tooLarge":
		return &backend.RejectedError{Stage: "DATA", Code: 552, Enhanced: "5.3.4", Message: msg}
	case "invalidEmail":
		return &backend.RejectedError{Stage: "DATA", Code: 554, Message: msg}
	default:
		return fmt.Errorf("jmap import: %s", msg)
	}
}

func submissionRejected(se *gojmap.SetError) *backend.RejectedError {
	msg := formatSetError(se)
	switch se.Type {
	case "rateLimit":
		return &backend.RejectedError{Stage: "MAIL", Code: 450, Enhanced: "4.7.0", Message: msg}
	case "overQuota":
		return &backend.RejectedError{Stage: "DATA", Code: 452, Enhanced: "4.2.2", Message: msg}
	case "notFound":
		return &backend.RejectedError{Stage: "MAIL", Code: 451, Enhanced: "4.3.0", Message: msg}
	case "forbidden", "forbiddenFrom", "forbiddenMailFrom", "forbiddenToSend":
		return &backend.RejectedError{Stage: "MAIL", Code: 550, Enhanced: "5.7.1", Message: msg}
	case "tooManyRecipients":
		return &backend.RejectedError{Stage: "RCPT", Code: 550, Enhanced: "5.5.3", Message: msg}
	case "invalidRecipients", "noRecipients":
		return &backend.RejectedError{Stage: "RCPT", Code: 550, Enhanced: "5.1.1", Message: msg}
	case "tooLarge":
		return &backend.RejectedError{Stage: "DATA", Code: 552, Enhanced: "5.3.4", Message: msg}
	case "invalidEmail", "invalidProperties":
		return &backend.RejectedError{Stage: "DATA", Code: 554, Message: msg}
	default:
		return &backend.RejectedError{Stage: "DATA", Code: 554, Message: msg}
	}
}

func formatSetError(se *gojmap.SetError) string {
	msg := se.Type
	if se.Description != nil && *se.Description != "" {
		msg += ": " + *se.Description
	} else if se.InvalidRecipients != nil && len(*se.InvalidRecipients) > 0 {
		msg += ": " + strings.Join(*se.InvalidRecipients, ", ")
	}
	if se.Properties != nil && len(*se.Properties) > 0 {
		msg += " [" + strings.Join(*se.Properties, ", ") + "]"
	}
	return msg
}

func headerFrom(raw []byte) string {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	addr, err := m.Header.AddressList("From")
	if err != nil || len(addr) == 0 {
		return ""
	}
	return addr[0].Address
}

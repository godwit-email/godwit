package jmap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gojmap "git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/jmapmemserver"
	"git.sr.ht/~rockorager/go-jmap/mail/email"
	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/jmap"
)

const testRaw = "From: Alice <alice@example.com>\r\nTo: Bob <bob@example.com>\r\nSubject: hello\r\nMessage-ID: <m1@example.com>\r\nDate: Mon, 02 Jan 2006 15:04:05 +0000\r\n\r\nhi bob\r\n"

type testServer struct {
	*jmapmemserver.Server
	drafts gojmap.ID
	sent   gojmap.ID
}

func newTestServer(t *testing.T, user string) *testServer {
	t.Helper()
	srv := jmapmemserver.New(user, "secret")
	t.Cleanup(srv.Close)
	srv.CreateMailbox("Inbox", mailbox.RoleInbox)
	drafts := srv.CreateMailbox("Drafts", mailbox.RoleDrafts)
	sent := srv.CreateMailbox("Sent", mailbox.RoleSent)
	return &testServer{Server: srv, drafts: drafts, sent: sent}
}

func newTestSender(srv *jmapmemserver.Server, token string) *jmap.Sender {
	return jmap.NewSender(jmap.Config{
		SessionURL: srv.URL() + "/.well-known/jmap",
		Credentials: func(context.Context) (backend.Credentials, error) {
			if token != "" {
				return backend.Credentials{Token: token}, nil
			}
			return backend.Credentials{Username: "alice@example.com", Password: "secret"}, nil
		},
	})
}

func mailboxEmails(t *testing.T, srv *jmapmemserver.Server, mbox gojmap.ID) []*email.Email {
	t.Helper()
	c := (&gojmap.Client{SessionEndpoint: srv.URL() + "/.well-known/jmap"}).WithAccessToken("secret")
	if err := c.Authenticate(); err != nil {
		t.Fatal(err)
	}
	req := &gojmap.Request{}
	q := req.Invoke(&email.Query{Account: srv.AccountID(), Filter: &email.FilterCondition{InMailbox: mbox}})
	req.Invoke(&email.Get{Account: srv.AccountID(), ReferenceIDs: &gojmap.ResultReference{ResultOf: q, Name: "Email/query", Path: "/ids"}})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Responses[1].Args.(*email.GetResponse).List
}

func TestSendSuccessAndBccOnlyInEnvelope(t *testing.T) {
	srv := newTestServer(t, "alice@example.com")
	srv.SeparateUploadHost()

	snd := newTestSender(srv.Server, "secret")
	if !snd.SavesSent() {
		t.Fatal("SavesSent() = false, want true")
	}

	err := snd.Send(t.Context(), "alice@example.com", []string{"bob@example.com", "secret-bcc@example.com"}, []byte(testRaw))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	subs := srv.Submissions()
	if len(subs) != 1 {
		t.Fatalf("got %d submissions, want 1", len(subs))
	}
	if subs[0].MailFrom != "alice@example.com" {
		t.Errorf("MailFrom = %q, want alice@example.com", subs[0].MailFrom)
	}
	if !slices.Equal(subs[0].RcptTo, []string{"bob@example.com", "secret-bcc@example.com"}) {
		t.Errorf("RcptTo = %v, want [bob@example.com secret-bcc@example.com]", subs[0].RcptTo)
	}
	if bytes.Contains(bytes.ToLower(subs[0].Raw), []byte("secret-bcc@example.com")) ||
		bytes.Contains(bytes.ToLower(subs[0].Raw), []byte("\nbcc:")) {
		t.Errorf("Bcc leaked into raw message headers: %q", subs[0].Raw)
	}

	if got := mailboxEmails(t, srv.Server, srv.drafts); len(got) != 0 {
		t.Errorf("Drafts has %d messages after send, want 0", len(got))
	}
	sentList := mailboxEmails(t, srv.Server, srv.sent)
	if len(sentList) != 1 {
		t.Fatalf("Sent has %d messages, want 1", len(sentList))
	}
	if sentList[0].Keywords["$draft"] || !sentList[0].Keywords["$seen"] {
		t.Errorf("Sent copy keywords = %+v, want $seen without $draft", sentList[0].Keywords)
	}
}

func TestSendIdentityMatchingAndNoIdentityRejected(t *testing.T) {
	srv := newTestServer(t, "alice@example.com")
	srv.SetIdentities("*@corp.example", "exact@other.example")
	snd := newTestSender(srv.Server, "secret")

	// Domain wildcard (*@corp.example) permits any sender at corp.example.
	corpRaw := strings.ReplaceAll(testRaw, "alice@example.com", "team@corp.example")
	if err := snd.Send(t.Context(), "team@corp.example", []string{"bob@example.com"}, []byte(corpRaw)); err != nil {
		t.Fatalf("domain wildcard identity Send: %v", err)
	}

	// Unmatched sender address is permanently rejected without creating any submission.
	err := snd.Send(t.Context(), "intruder@nope.example", []string{"bob@example.com"}, []byte(testRaw))
	var re *backend.RejectedError
	if !errors.As(err, &re) || re.Code < 500 {
		t.Fatalf("unmatched identity Send = %v, want permanent *backend.RejectedError", err)
	}
	if len(srv.Submissions()) != 1 {
		t.Errorf("submissions = %d, want 1", len(srv.Submissions()))
	}
}

func TestSendNotCreatedMapsToRejectedErrorAndCleansStagedDraft(t *testing.T) {
	for _, tc := range []struct {
		typ       string
		desc      string
		wantStage string
	}{
		{"forbiddenFrom", "From address not allowed", "MAIL"},
		{"forbiddenToSend", "account suspended from sending", "MAIL"},
		{"tooManyRecipients", "over 100 recipients", "RCPT"},
		{"invalidEmail", "missing Date header", "DATA"},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			srv := newTestServer(t, "alice@example.com")
			desc := tc.desc
			srv.FailNextSubmission(&gojmap.SetError{Type: tc.typ, Description: &desc})

			err := newTestSender(srv.Server, "secret").Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw))
			var re *backend.RejectedError
			if !errors.As(err, &re) {
				t.Fatalf("Send = %v, want *backend.RejectedError", err)
			}
			if re.Code < 500 || re.Stage != tc.wantStage {
				t.Errorf("RejectedError = %+v, want stage %s and 5xx", re, tc.wantStage)
			}
			if !strings.Contains(re.Message, tc.typ) || !strings.Contains(re.Message, tc.desc) {
				t.Errorf("RejectedError.Message = %q, want %q and %q", re.Message, tc.typ, tc.desc)
			}
			if got := mailboxEmails(t, srv.Server, srv.drafts); len(got) != 0 {
				t.Errorf("Drafts has %d leftover staged emails, want 0", len(got))
			}
			if got := mailboxEmails(t, srv.Server, srv.sent); len(got) != 0 {
				t.Errorf("Sent has %d emails after rejected submission, want 0", len(got))
			}
		})
	}
}

func TestSendUncertainOnDroppedResponseOr5xx(t *testing.T) {
	t.Run("dropped connection", func(t *testing.T) {
		srv := newTestServer(t, "alice@example.com")
		srv.LoseNextSubmission()
		err := newTestSender(srv.Server, "secret").Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw))
		if !errors.Is(err, backend.ErrUncertain) {
			t.Fatalf("Send on dropped response = %v, want ErrUncertain", err)
		}
	})

	t.Run("HTTP 502 on submission", func(t *testing.T) {
		srv := newTestServer(t, "alice@example.com")
		srv.StatusNextSubmission(http.StatusBadGateway)
		err := newTestSender(srv.Server, "secret").Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw))
		if !errors.Is(err, backend.ErrUncertain) {
			t.Fatalf("Send on HTTP 502 = %v, want ErrUncertain", err)
		}
	})

	t.Run("upload failure before submission is retryable", func(t *testing.T) {
		srv := newTestServer(t, "alice@example.com")
		srv.Redirect("/jmap/upload/"+string(srv.AccountID()), "https://untrusted.example/upload")
		err := newTestSender(srv.Server, "secret").Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw))
		if err == nil || errors.Is(err, backend.ErrUncertain) || errors.As(err, new(*backend.RejectedError)) {
			t.Fatalf("Send on failed upload = %v, want plain retryable error", err)
		}
	})
}

// A server that never answers costs a send its deadline. The error names the
// request and its server: JMAP has no wait that returns the bare context error (#755).
func TestSendTimeoutNamesHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := jmap.NewSender(jmap.Config{SessionURL: srv.URL + "/session", Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Token: "secret"}, nil
	}}).Send(ctx, "alice@example.com", []string{"bob@example.com"}, []byte(testRaw))
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), strings.TrimPrefix(srv.URL, "http://")) {
		t.Fatalf("Send = %v, want the deadline with the server it was for", err)
	}
}

func TestNoSubmissionCapability(t *testing.T) {
	srv := newTestServer(t, "alice@example.com")
	srv.DisableSubmission()

	recv := jmap.New(jmap.Config{
		SessionURL: srv.URL() + "/.well-known/jmap",
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Token: "secret"}, nil
		},
	})
	defer recv.Close()

	res, err := recv.Check(t.Context())
	if !errors.Is(err, jmap.ErrNoSubmission) {
		t.Fatalf("Check without submission capability = %v, want ErrNoSubmission", err)
	}
	if res.Host == "" || res.Folders == 0 {
		t.Errorf("Check without submission = %+v, want the receiving result with the error", res)
	}
	desc, err := recv.Describe(t.Context())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got := desc.Features[backend.FeatureSavesSent]; got.Level != backend.LevelUnsupported || got.Reason != jmap.ReasonNoSubmission {
		t.Errorf("Describe FeatureSavesSent = %+v, want LevelUnsupported with %q", got, jmap.ReasonNoSubmission)
	}

	err = newTestSender(srv.Server, "secret").Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw))
	if !errors.Is(err, jmap.ErrNoSubmission) {
		t.Fatalf("Send without submission capability = %v, want ErrNoSubmission", err)
	}
	var re *backend.RejectedError
	if !errors.As(err, &re) || re.Code < 500 || re.Message != jmap.ReasonNoSubmission {
		t.Fatalf("Send without submission capability = %v, want permanent *backend.RejectedError saying %s", err, jmap.ReasonNoSubmission)
	}
}

func TestSendHonorsSentFolderOverride(t *testing.T) {
	t.Run("local Sent role override without server RoleSent", func(t *testing.T) {
		srv := jmapmemserver.New("alice@example.com", "secret")
		t.Cleanup(srv.Close)
		srv.CreateMailbox("Inbox", mailbox.RoleInbox)
		drafts := srv.CreateMailbox("Drafts", mailbox.RoleDrafts)
		customSent := srv.CreateMailbox("FiledSent", "")

		override := backend.FolderRef(customSent)
		snd := jmap.NewSender(jmap.Config{
			SessionURL: srv.URL() + "/.well-known/jmap",
			Credentials: func(context.Context) (backend.Credentials, error) {
				return backend.Credentials{Token: "secret"}, nil
			},
			SentFolder: &override,
		})
		if err := snd.Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw)); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if !snd.SavesSent() {
			t.Fatal("SavesSent() = false, want true")
		}
		if got := mailboxEmails(t, srv, drafts); len(got) != 0 {
			t.Errorf("Drafts has %d messages, want 0", len(got))
		}
		sentList := mailboxEmails(t, srv, customSent)
		if len(sentList) != 1 {
			t.Fatalf("custom Sent mailbox has %d messages, want 1", len(sentList))
		}
		if sentList[0].Keywords["$draft"] || !sentList[0].Keywords["$seen"] {
			t.Errorf("custom Sent copy keywords = %+v, want $seen without $draft", sentList[0].Keywords)
		}
	})

	t.Run("cleared Sent role destroys staged copy instead of using server RoleSent", func(t *testing.T) {
		srv := newTestServer(t, "alice@example.com")
		none := backend.FolderRef("")
		snd := jmap.NewSender(jmap.Config{
			SessionURL: srv.URL() + "/.well-known/jmap",
			Credentials: func(context.Context) (backend.Credentials, error) {
				return backend.Credentials{Token: "secret"}, nil
			},
			SentFolder: &none,
		})
		if err := snd.Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw)); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if !snd.SavesSent() {
			t.Fatal("SavesSent() = false, want true when account has no Sent folder")
		}
		if got := mailboxEmails(t, srv.Server, srv.drafts); len(got) != 0 {
			t.Errorf("Drafts has %d messages, want 0", len(got))
		}
		if got := mailboxEmails(t, srv.Server, srv.sent); len(got) != 0 {
			t.Errorf("server RoleSent has %d messages, want 0 when Sent role was cleared", len(got))
		}
	})
}

func TestSendOnSuccessUpdateFailureMarksUnsavedAndCleansDraft(t *testing.T) {
	srv := newTestServer(t, "alice@example.com")
	var proxy *httptest.Server
	proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/jmap/api" && bytes.Contains(body, []byte("EmailSubmission/set")) {
			body = bytes.ReplaceAll(body, []byte("mailboxIds/"+string(srv.sent)), []byte("mailboxIds/deleted-sent"))
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, srv.URL()+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		out = bytes.ReplaceAll(out, []byte(srv.URL()), []byte(proxy.URL))
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
	}))
	t.Cleanup(proxy.Close)

	snd := jmap.NewSender(jmap.Config{
		SessionURL: proxy.URL + "/jmap/session",
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Token: "secret"}, nil
		},
	})
	if err := snd.Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw)); err != nil {
		t.Fatalf("Send when onSuccessUpdateEmail fails = %v, want nil (already accepted)", err)
	}
	if snd.SavesSent() {
		t.Fatal("SavesSent() = true after failed onSuccessUpdateEmail, want false")
	}
	if len(srv.Submissions()) != 1 {
		t.Fatalf("submissions = %d, want 1", len(srv.Submissions()))
	}
	if got := mailboxEmails(t, srv.Server, srv.drafts); len(got) != 0 {
		t.Errorf("Drafts has %d leftover staged messages, want 0", len(got))
	}
	if got := mailboxEmails(t, srv.Server, srv.sent); len(got) != 0 {
		t.Errorf("Sent has %d messages before recovery, want 0", len(got))
	}
}

func TestSendTransientNotCreatedIsRetryable(t *testing.T) {
	for _, typ := range []string{"rateLimit", "overQuota", "notFound"} {
		t.Run(typ, func(t *testing.T) {
			srv := newTestServer(t, "alice@example.com")
			desc := "try again later"
			srv.FailNextSubmission(&gojmap.SetError{Type: typ, Description: &desc})

			err := newTestSender(srv.Server, "secret").Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw))
			if errors.Is(err, backend.ErrUncertain) {
				t.Fatalf("Send on %s = %v, must not be ErrUncertain", typ, err)
			}
			var re *backend.RejectedError
			if !errors.As(err, &re) || re.Code < 400 || re.Code >= 500 {
				t.Fatalf("Send on %s = %v, want 4xx *backend.RejectedError", typ, err)
			}
			if got := mailboxEmails(t, srv.Server, srv.drafts); len(got) != 0 {
				t.Errorf("Drafts has %d leftover staged emails, want 0", len(got))
			}
		})
	}
}

func TestSendServerPartialFailReturnsUncertainAndPreservesEvidence(t *testing.T) {
	srv := newTestServer(t, "alice@example.com")
	var proxy *httptest.Server
	proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, err := http.NewRequestWithContext(r.Context(), r.Method, srv.URL()+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if r.URL.Path == "/jmap/api" && bytes.Contains(body, []byte("EmailSubmission/set")) {
			var wire struct {
				MethodResponses [][3]json.RawMessage `json:"methodResponses"`
				SessionState    string               `json:"sessionState"`
			}
			if err := json.Unmarshal(out, &wire); err != nil {
				t.Fatal(err)
			}
			for i := range wire.MethodResponses {
				var name string
				_ = json.Unmarshal(wire.MethodResponses[i][0], &name)
				if name == "EmailSubmission/set" {
					wire.MethodResponses[i][0] = json.RawMessage(`"error"`)
					wire.MethodResponses[i][1] = json.RawMessage(`{"type":"serverPartialFail"}`)
					wire.MethodResponses = wire.MethodResponses[:i+1]
					break
				}
			}
			out, _ = json.Marshal(wire)
		}
		out = bytes.ReplaceAll(out, []byte(srv.URL()), []byte(proxy.URL))
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
	}))
	t.Cleanup(proxy.Close)

	snd := jmap.NewSender(jmap.Config{
		SessionURL: proxy.URL + "/jmap/session",
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Token: "secret"}, nil
		},
	})
	err := snd.Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw))
	if !errors.Is(err, backend.ErrUncertain) {
		t.Fatalf("Send on serverPartialFail = %v, want ErrUncertain", err)
	}
	// Sent copy created before serverPartialFail must not be destroyed by cleanupStaged.
	if got := mailboxEmails(t, srv.Server, srv.sent); len(got) != 1 {
		t.Fatalf("Sent has %d messages after serverPartialFail, want 1 preserved for recovery", len(got))
	}
}

// proxySender is a Sender reaching srv through a proxy: intercept answers a
// request itself when it returns true, else the request goes on to srv.
func proxySender(t *testing.T, srv *jmapmemserver.Server, intercept func(w http.ResponseWriter, r *http.Request, body []byte) bool) *jmap.Sender {
	var proxy *httptest.Server
	proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if intercept(w, r, body) {
			return
		}
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, srv.URL()+r.URL.RequestURI(), bytes.NewReader(body))
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(bytes.ReplaceAll(out, []byte(srv.URL()), []byte(proxy.URL)))
	}))
	t.Cleanup(proxy.Close)
	return jmap.NewSender(jmap.Config{
		SessionURL:  proxy.URL + "/jmap/session",
		Credentials: func(context.Context) (backend.Credentials, error) { return backend.Credentials{Token: "secret"}, nil },
	})
}

// Without a Drafts mailbox the copy is staged outside Sent: a server that
// stops after Email/import, before the submission, leaves nothing in Sent
// that would pass for the sent copy.
func TestSendWithoutDraftsNeverStagesInSent(t *testing.T) {
	srv := jmapmemserver.New("alice@example.com", "secret")
	t.Cleanup(srv.Close)
	inbox := srv.CreateMailbox("Inbox", mailbox.RoleInbox)
	sent := srv.CreateMailbox("Sent", mailbox.RoleSent)
	var cut atomic.Bool
	cut.Store(true)
	snd := proxySender(t, srv, func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if !bytes.Contains(body, []byte("EmailSubmission/set")) || !cut.CompareAndSwap(true, false) {
			return false
		}
		var req struct {
			Using       []string             `json:"using"`
			MethodCalls [][3]json.RawMessage `json:"methodCalls"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
			return false
		}
		req.MethodCalls = req.MethodCalls[:1] // Email/import alone runs
		b, _ := json.Marshal(req)
		fwd, _ := http.NewRequestWithContext(r.Context(), r.Method, srv.URL()+r.URL.RequestURI(), bytes.NewReader(b))
		fwd.Header = r.Header.Clone()
		if resp, err := http.DefaultClient.Do(fwd); err == nil {
			resp.Body.Close()
		}
		w.WriteHeader(http.StatusBadGateway)
		return true
	})

	err := snd.Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw))
	if !errors.Is(err, backend.ErrUncertain) {
		t.Fatalf("Send cut off after Email/import = %v, want ErrUncertain", err)
	}
	if got := mailboxEmails(t, srv, sent); len(got) != 0 {
		t.Fatalf("Sent has %d messages after an uncertain send, want none", len(got))
	}
	if got := mailboxEmails(t, srv, inbox); len(got) != 1 || !got[0].Keywords["$draft"] {
		t.Fatalf("Inbox = %+v, want the staged $draft copy", got)
	}

	// Sent normally, the copy moves from where it was staged to Sent.
	srv2 := jmapmemserver.New("alice@example.com", "secret")
	t.Cleanup(srv2.Close)
	inbox2 := srv2.CreateMailbox("Inbox", mailbox.RoleInbox)
	sent2 := srv2.CreateMailbox("Sent", mailbox.RoleSent)
	if err := newTestSender(srv2, "secret").Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := mailboxEmails(t, srv2, sent2); len(got) != 1 || got[0].Keywords["$draft"] {
		t.Fatalf("Sent = %+v, want the one sent copy", got)
	}
	if got := mailboxEmails(t, srv2, inbox2); len(got) != 0 {
		t.Fatalf("Inbox keeps %d staged copies, want none", len(got))
	}
}

// A server that refuses duplicate imports answers a resend, after an
// uncertain send left its staged copy, with alreadyExists: the staged copy
// is dropped and the message submitted once.
func TestSendResendDropsStagedCopyOnAlreadyExists(t *testing.T) {
	srv := newTestServer(t, "alice@example.com")
	srv.AddEmail([]gojmap.ID{srv.drafts}, []byte(testRaw), "$draft", "$seen") // left by the uncertain send
	var refuse atomic.Bool
	refuse.Store(true)
	snd := proxySender(t, srv.Server, func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if !bytes.Contains(body, []byte("Email/import")) || !refuse.CompareAndSwap(true, false) {
			return false
		}
		acct := string(srv.AccountID())
		_, _ = io.WriteString(w, `{"methodResponses":[["Email/import",{"accountId":"`+acct+`","notCreated":{"m":{"type":"alreadyExists"}}},"0"],`+
			`["EmailSubmission/set",{"accountId":"`+acct+`","notCreated":{"s":{"type":"invalidProperties"}}},"1"]],"sessionState":"0"}`)
		return true
	})

	if err := snd.Send(t.Context(), "alice@example.com", []string{"bob@example.com"}, []byte(testRaw)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if n := len(srv.Submissions()); n != 1 {
		t.Fatalf("%d submissions, want 1", n)
	}
	if got := mailboxEmails(t, srv.Server, srv.drafts); len(got) != 0 {
		t.Errorf("Drafts keeps %d staged copies, want none", len(got))
	}
	if got := mailboxEmails(t, srv.Server, srv.sent); len(got) != 1 {
		t.Errorf("Sent has %d copies, want 1", len(got))
	}
}

package jmap_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

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

	if _, err := recv.Check(t.Context()); !errors.Is(err, jmap.ErrNoSubmission) {
		t.Fatalf("Check without submission capability = %v, want ErrNoSubmission", err)
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
	if !errors.As(err, &re) || re.Code < 500 {
		t.Fatalf("Send without submission capability = %v, want permanent *backend.RejectedError", err)
	}
}

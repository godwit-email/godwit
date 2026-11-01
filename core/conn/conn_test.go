package conn_test

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fake starts a raw TCP server; handle runs per connection.
func fake(t *testing.T, handle func(net.Conn)) conn.Endpoint {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); handle(c) }()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return conn.Endpoint{Host: "127.0.0.1", Port: a.Port, Security: conn.None, Username: "alice", Password: "hunter2"}
}

func stall(c net.Conn) {
	for {
		if _, err := c.Read(make([]byte, 512)); err != nil {
			return
		}
	}
} // never answers; returns when the client closes

func returnsPromptly(t *testing.T, dial func(ctx context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() { done <- dial(ctx) }()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("dial did not return after ctx cancellation")
	}
}

func TestDialCancelWhileStalled(t *testing.T) {
	silent := fake(t, stall)
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialIMAP(ctx, silent); return err })
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialSMTP(ctx, silent); return err })

	// greeting arrives, but LOGIN / AUTH are never answered
	imapHalf := fake(t, func(c net.Conn) { c.Write([]byte("* OK hi\r\n")); stall(c) })
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialIMAP(ctx, imapHalf); return err })
	smtpHalf := fake(t, func(c net.Conn) {
		c.Write([]byte("220 hi\r\n"))
		r := bufio.NewReader(c)
		r.ReadString('\n') // EHLO
		c.Write([]byte("250-hi\r\n250 AUTH PLAIN\r\n"))
		stall(c)
	})
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialSMTP(ctx, smtpHalf); return err })
}

func TestAuthErrorsDoNotLeakServerText(t *testing.T) {
	imapEcho := fake(t, func(c net.Conn) {
		c.Write([]byte("* OK hi\r\n"))
		r := bufio.NewReader(c)
		for { // the client may ask for CAPABILITY before LOGIN
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) > 1 && strings.EqualFold(f[1], "LOGIN") {
				c.Write([]byte(f[0] + " NO login failed for password hunter2\r\n"))
				stall(c)
				return
			}
			c.Write([]byte("* CAPABILITY IMAP4rev1\r\n" + f[0] + " OK done\r\n"))
		}
	})
	_, err := conn.DialIMAP(context.Background(), imapEcho)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
	assert.NotContains(t, err.Error(), "hunter2")

	smtpEcho := fake(t, func(c net.Conn) {
		c.Write([]byte("220 hi\r\n"))
		r := bufio.NewReader(c)
		r.ReadString('\n')
		c.Write([]byte("250-hi\r\n250 AUTH PLAIN\r\n"))
		r.ReadString('\n')
		c.Write([]byte("535 5.7.8 password hunter2 rejected\r\n"))
	})
	_, err = conn.DialSMTP(context.Background(), smtpEcho)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
	assert.NotContains(t, err.Error(), "hunter2")
}

func TestMalformedAuthResponsesDoNotLeakPassword(t *testing.T) {
	imapBad := fake(t, func(c net.Conn) {
		c.Write([]byte("* OK hi\r\n"))
		bufio.NewReader(c).ReadString('\n')
		c.Write([]byte("hunter2 NO rejected\r\n")) // unknown tag line echoing the password
	})
	_, err := conn.DialIMAP(context.Background(), imapBad)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2")

	smtpBad := fake(t, func(c net.Conn) {
		c.Write([]byte("220 hi\r\n"))
		r := bufio.NewReader(c)
		r.ReadString('\n')
		c.Write([]byte("250-hi\r\n250 AUTH PLAIN\r\n"))
		r.ReadString('\n')
		c.Write([]byte("hunter2 is not a reply code\r\n"))
	})
	_, err = conn.DialSMTP(context.Background(), smtpBad)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2")
}

func TestDialCancelDuringStalledTLSHandshake(t *testing.T) {
	silent := fake(t, stall) // accepts, never answers the ClientHello
	silent.Security = conn.TLS
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialIMAP(ctx, silent); return err })
	returnsPromptly(t, func(ctx context.Context) error { _, err := conn.DialSMTP(ctx, silent); return err })
}

func TestTLSHandshakeFailureIsNotReportedAsCancel(t *testing.T) {
	plain := fake(t, func(c net.Conn) {
		c.Write([]byte("this is not a TLS server\r\n"))
		stall(c)
	})
	plain.Security = conn.TLS
	for name, dial := range map[string]func() error{
		"imap": func() error { _, err := conn.DialIMAP(context.Background(), plain); return err },
		"smtp": func() error { _, err := conn.DialSMTP(context.Background(), plain); return err },
	} {
		err := dial()
		require.Error(t, err, name)
		assert.NotErrorIs(t, err, context.Canceled, name)
		assert.NotErrorIs(t, err, context.DeadlineExceeded, name)
	}
}

// A STARTTLS port set to SSL/TLS (#406) is retried with STARTTLS, never in plaintext.
func TestTLSOnPlaintextPortRetriesStartTLS(t *testing.T) {
	for _, proto := range []string{"imap", "smtp"} {
		var dials atomic.Int32
		var mu sync.Mutex
		var seen strings.Builder
		e := fake(t, func(c net.Conn) {
			dials.Add(1)
			if proto == "imap" {
				c.Write([]byte("* OK hi\r\n"))
			} else {
				c.Write([]byte("220 hi\r\n"))
			}
			r := bufio.NewReader(c)
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				mu.Lock()
				seen.WriteString(l)
				mu.Unlock()
				if f := strings.Fields(l); proto == "imap" && len(f) > 0 {
					c.Write([]byte(f[0] + " NO no TLS here\r\n"))
				} else {
					c.Write([]byte("250 hi\r\n")) // no STARTTLS offered
				}
			}
		})
		e.Security = conn.TLS
		var err error
		if proto == "imap" {
			_, err = conn.DialIMAP(context.Background(), e)
		} else {
			_, err = conn.DialSMTP(context.Background(), e)
		}
		require.Error(t, err, proto)
		assert.EqualValues(t, 2, dials.Load(), proto)
		mu.Lock()
		joined := seen.String()
		mu.Unlock()
		assert.NotContains(t, joined, "hunter2", proto)
		assert.NotContains(t, joined, "AUTH", proto)
		assert.NotContains(t, joined, "LOGIN", proto)
	}
}

func TestSMTPLoginWhenPlainNotOffered(t *testing.T) {
	addr, in := testmail.StartSMTP(t, testmail.LoginOnly())
	e := endpoint(t, addr)
	c, err := conn.DialSMTP(context.Background(), e)
	require.NoError(t, err)
	c.Close()

	e.Password = "wrong"
	_, err = conn.DialSMTP(context.Background(), e)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
	assert.Equal(t, 2, in.AuthAttempts())
}

func endpoint(t *testing.T, addr string) conn.Endpoint {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	p, err := strconv.Atoi(port)
	require.NoError(t, err)
	return conn.Endpoint{Host: host, Port: p, Security: conn.None, Username: "alice", Password: "secret"}
}

func TestXOAuth2IMAPAndSMTP(t *testing.T) {
	ctx := context.Background()
	im := testmail.StartIMAP(t, testmail.WithXOAuth2("tok"))
	smtpAddr, _ := testmail.StartSMTP(t, testmail.WithXOAuth2("tok"))
	ie, se := endpoint(t, im.Addr), endpoint(t, smtpAddr)
	ie.Password, se.Password = "wrong", "wrong" // ignored when AccessToken is set
	ie.AccessToken, se.AccessToken = "tok", "tok"

	c, err := conn.DialIMAP(ctx, ie)
	require.NoError(t, err)
	_, err = c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	c.Close()
	assert.EqualValues(t, 1, im.Logins())
	s, err := conn.DialSMTP(ctx, se)
	require.NoError(t, err)
	s.Close()

	ie.AccessToken, se.AccessToken = "bad", "bad"
	_, err = conn.DialIMAP(ctx, ie)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
	assert.NotContains(t, err.Error(), "bad")
	assert.EqualValues(t, 2, im.Logins())
	_, err = conn.DialSMTP(ctx, se)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
	assert.NotContains(t, err.Error(), "bad")

	// servers without XOAUTH2
	plainIMAP := testmail.StartIMAP(t)
	plainSMTP, _ := testmail.StartSMTP(t)
	ie, se = endpoint(t, plainIMAP.Addr), endpoint(t, plainSMTP)
	ie.AccessToken, se.AccessToken = "tok", "tok"
	_, err = conn.DialIMAP(ctx, ie)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
	_, err = conn.DialSMTP(ctx, se)
	require.ErrorIs(t, err, conn.ErrAuthFailed)
}

func TestXOAuth2InitialResponse(t *testing.T) {
	mech, ir, err := conn.NewXOAuth2Client("alice", "tok").Start()
	require.NoError(t, err)
	assert.Equal(t, "XOAUTH2", mech)
	assert.Equal(t, "user=alice\x01auth=Bearer tok\x01\x01", string(ir))
	resp, err := conn.NewXOAuth2Client("alice", "tok").Next([]byte(`{"status":"401"}`))
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp)
}

func TestIMAPSendsIDWhenAdvertised(t *testing.T) {
	im := testmail.StartIMAP(t, testmail.RequireID())

	// without ID the server refuses SELECT, like NetEase
	raw, err := imapclient.DialInsecure(im.Addr, nil)
	require.NoError(t, err)
	require.NoError(t, raw.Login("alice", "secret").Wait())
	_, err = raw.Select("INBOX", nil).Wait()
	require.Error(t, err)
	raw.Close()

	c, err := conn.DialIMAP(context.Background(), endpoint(t, im.Addr))
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	ids := im.IDs()
	require.Len(t, ids, 1)
	assert.Equal(t, "Godwit", ids[0]["name"])
	assert.Equal(t, conn.ClientVersion, ids[0]["version"])
}

func TestIDFailureIsNotLoginFailure(t *testing.T) {
	var gotID atomic.Bool
	e := fake(t, func(c net.Conn) {
		c.Write([]byte("* OK [CAPABILITY IMAP4rev1 ID] hi\r\n"))
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				return
			}
			switch strings.ToUpper(f[1]) {
			case "ID":
				gotID.Store(true)
				c.Write([]byte(f[0] + " BAD ID not today\r\n"))
			case "LOGIN":
				c.Write([]byte(f[0] + " OK [CAPABILITY IMAP4rev1 ID] logged in\r\n"))
			case "LOGOUT":
				c.Write([]byte("* BYE\r\n" + f[0] + " OK bye\r\n"))
				return
			default: // CAPABILITY, NOOP
				c.Write([]byte("* CAPABILITY IMAP4rev1 ID\r\n" + f[0] + " OK done\r\n"))
			}
		}
	})
	c, err := conn.DialIMAP(context.Background(), e)
	require.NoError(t, err)
	c.Close()
	assert.True(t, gotID.Load())
}

func TestIMAPEmptyLiteralWithoutSpace(t *testing.T) {
	e := fake(t, func(c net.Conn) {
		c.Write([]byte("* OK [CAPABILITY IMAP4rev1] hi\r\n"))
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				return
			}
			switch strings.ToUpper(f[1]) {
			case "LOGIN":
				c.Write([]byte(f[0] + " OK [CAPABILITY IMAP4rev1] logged in\r\n"))
			case "SELECT":
				c.Write([]byte("* 2 EXISTS\r\n* OK [UIDVALIDITY 1] ok\r\n" + f[0] + " OK [READ-ONLY] selected\r\n"))
			case "FETCH", "UID":
				// Strato-style response: no space between empty literal "{0}\r\n" and "UID".
				c.Write([]byte("* 1 FETCH (BODY[HEADER.FIELDS (REFERENCES)] {0}\r\n"))
				c.Write([]byte("UID 42)\r\n"))
				// A body that merely contains "{0}\r\nFoo" must pass through untouched.
				c.Write([]byte("* 2 FETCH (UID 43 BODY[HEADER.FIELDS (REFERENCES)] {8}\r\n{0}\r"))
				c.Write([]byte("\nFoo)\r\n" + f[0] + " OK fetch completed\r\n"))
			case "LOGOUT":
				c.Write([]byte("* BYE\r\n" + f[0] + " OK bye\r\n"))
				return
			default:
				c.Write([]byte(f[0] + " OK done\r\n"))
			}
		}
	})
	c, err := conn.DialIMAP(context.Background(), e)
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	require.NoError(t, err)
	sec := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{"REFERENCES"},
		Peek:         true,
	}
	msgs, err := c.Fetch(imap.SeqSetNum(1, 2), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{sec},
	}).Collect()
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, imap.UID(42), msgs[0].UID)
	assert.Equal(t, imap.UID(43), msgs[1].UID)
	assert.Equal(t, "{0}\r\nFoo", string(msgs[1].FindBodySection(sec)))
}

// TestIMAPTencentExmail fakes imap.exmail.qq.com: a broken charset parameter in
// BODYSTRUCTURE (#387) and an empty section for a quoted HEADER.FIELDS list (#388).
func TestIMAPTencentExmail(t *testing.T) {
	e := fake(t, func(c net.Conn) {
		c.Write([]byte("* OK [CAPABILITY IMAP4rev1] hi\r\n"))
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				return
			}
			switch strings.ToUpper(f[1]) {
			case "LOGIN":
				c.Write([]byte(f[0] + " OK [CAPABILITY IMAP4rev1] logged in\r\n"))
			case "SELECT":
				c.Write([]byte("* 1 EXISTS\r\n* OK [UIDVALIDITY 1] ok\r\n" + f[0] + " OK [READ-ONLY] selected\r\n"))
			case "FETCH", "UID":
				hdr := "\r\n"
				if strings.Contains(line, "HEADER.FIELDS (SUBJECT FROM)") {
					hdr = "Subject: hi\r\nFrom: a@example.com\r\n\r\n"
				}
				resp := `* 1 FETCH (UID 7 BODYSTRUCTURE (("TEXT" "PLAIN" ("charset " " "utf-8"") NIL NIL "8BIT" 588 44 NIL NIL NIL)("TEXT" "HTML" ("charset " " "utf-8"") NIL NIL "8BIT" 2670 54 NIL NIL NIL) "ALTERNATIVE" ("BOUNDARY" "b1") NIL NIL) ` +
					"BODY[HEADER.FIELDS (SUBJECT FROM)] {" + strconv.Itoa(len(hdr)) + "}\r\n" + hdr + ")\r\n" + f[0] + " OK FETCH Completed\r\n"
				for i := range len(resp) { // byte by byte, to split the groups across reads
					c.Write([]byte{resp[i]})
				}
			case "LOGOUT":
				c.Write([]byte("* BYE\r\n" + f[0] + " OK bye\r\n"))
				return
			default:
				c.Write([]byte(f[0] + " OK done\r\n"))
			}
		}
	})
	c, err := conn.DialIMAP(context.Background(), e)
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	require.NoError(t, err)
	sec := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"SUBJECT", "FROM"}, Peek: true}
	msgs, err := c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imap.FetchItemBodySection{sec},
	}).Collect()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	mp, ok := msgs[0].BodyStructure.(*imap.BodyStructureMultiPart)
	require.True(t, ok)
	require.Len(t, mp.Children, 2)
	for _, ch := range mp.Children {
		assert.Equal(t, "utf-8", ch.(*imap.BodyStructureSinglePart).Params["charset"])
	}
	assert.Equal(t, "Subject: hi\r\nFrom: a@example.com\r\n\r\n", string(msgs[0].FindBodySection(sec)))
}

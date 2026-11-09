package testmail_test

import (
	"context"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-smtp"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

const rfc822 = "From: a@b.c\r\nTo: alice@x.y\r\nSubject: hi\r\n\r\nbody\r\n"

func dial(t *testing.T, s *testmail.IMAP) *imapclient.Client {
	t.Helper()
	host, port := split(t, s.Addr)
	c, err := conn.DialIMAP(ctx, conn.Endpoint{Host: host, Port: port, Security: conn.None, Username: "alice", Password: "secret"})
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

func split(t *testing.T, addr string) (string, int) {
	t.Helper()
	i := strings.LastIndex(addr, ":")
	var p int
	for _, ch := range addr[i+1:] {
		p = p*10 + int(ch-'0')
	}
	return addr[:i], p
}

func seed(t *testing.T, s *testmail.IMAP, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := s.User.Append("INBOX", bytesLiteral(rfc822), &imap.AppendOptions{})
		require.NoError(t, err)
	}
}

func TestDefaultCaps(t *testing.T) {
	c := dial(t, testmail.StartIMAP(t))
	assert.True(t, c.Caps().Has(imap.CapMove))
	assert.True(t, c.Caps().Has(imap.CapUIDPlus))
	assert.True(t, c.Caps().Has(imap.CapIdle))
}

func TestWithoutIdleAndLogins(t *testing.T) {
	s := testmail.StartIMAP(t, testmail.WithoutIdle())
	c := dial(t, s)
	assert.False(t, c.Caps().Has(imap.CapIdle))
	assert.True(t, c.Caps().Has(imap.CapMove))
	assert.Equal(t, int64(1), s.Logins())
}

func TestWithoutMove(t *testing.T) {
	c := dial(t, testmail.StartIMAP(t, testmail.WithoutMove()))
	assert.False(t, c.Caps().Has(imap.CapMove))
	assert.True(t, c.Caps().Has(imap.CapUIDPlus))
}

func TestWithoutUIDPlusSuppressesCopyUID(t *testing.T) {
	s := testmail.StartIMAP(t, testmail.WithoutUIDPlus())
	seed(t, s, 2)
	c := dial(t, s)
	assert.False(t, c.Caps().Has(imap.CapUIDPlus))
	_, err := c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	cp, err := c.Copy(imap.UIDSetNum(1), "Archive").Wait()
	require.NoError(t, err)
	assert.Zero(t, cp.UIDValidity)
	assert.Empty(t, cp.DestUIDs)
	mv, err := c.Move(imap.UIDSetNum(2), "Trash").Wait()
	require.NoError(t, err)
	assert.Zero(t, mv.UIDValidity)
	assert.Empty(t, mv.DestUIDs)
}

func TestCopyUIDByDefault(t *testing.T) {
	s := testmail.StartIMAP(t)
	seed(t, s, 1)
	c := dial(t, s)
	_, err := c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	cp, err := c.Copy(imap.UIDSetNum(1), "Archive").Wait()
	require.NoError(t, err)
	assert.NotEmpty(t, cp.DestUIDs)
}

func TestDropNextFetch(t *testing.T) {
	s := testmail.StartIMAP(t)
	seed(t, s, 5)
	c := dial(t, s)
	_, err := c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	s.DropNextFetch()
	_, err = c.Fetch(imap.SeqSet{{Start: 1, Stop: 5}}, &imap.FetchOptions{Flags: true, UID: true}).Collect()
	assert.Error(t, err)
	// next connection works normally
	c2 := dial(t, s)
	_, err = c2.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	msgs, err := c2.Fetch(imap.SeqSet{{Start: 1, Stop: 5}}, &imap.FetchOptions{UID: true}).Collect()
	require.NoError(t, err)
	assert.Len(t, msgs, 5)
}

func TestStopRestart(t *testing.T) {
	s := testmail.StartIMAP(t)
	seed(t, s, 1)
	c := dial(t, s)
	s.Stop()
	assert.Error(t, c.Noop().Wait())
	host, port := split(t, s.Addr)
	_, err := conn.DialIMAP(ctx, conn.Endpoint{Host: host, Port: port, Security: conn.None, Username: "alice", Password: "secret"})
	assert.Error(t, err)
	s.Restart()
	c2 := dial(t, s)
	sel, err := c2.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	assert.Equal(t, uint32(1), sel.NumMessages)
}

func TestSMTPFailAfterData(t *testing.T) {
	addr, in := testmail.StartSMTP(t)
	host, port := split(t, addr)
	ep := conn.Endpoint{Host: host, Port: port, Security: conn.None, Username: "alice", Password: "secret"}
	send := func() error {
		c, err := conn.DialSMTP(ctx, ep)
		require.NoError(t, err)
		defer c.Close()
		return c.SendMail("a@b.c", []string{"d@e.f"}, strings.NewReader(rfc822))
	}
	require.NoError(t, send())
	require.Len(t, in.Messages(), 1)
	assert.Equal(t, []string{"d@e.f"}, in.Messages()[0].To)
	in.FailAfterData(1)
	assert.Error(t, send())
	assert.Len(t, in.Messages(), 2, "the server got it even though it never replied")
	require.NoError(t, send())
	assert.Len(t, in.Messages(), 3)

	ep.Password = "nope"
	_, err := conn.DialSMTP(ctx, ep)
	assert.Error(t, err)
}

// smtpSend sends rfc822 from a@b.c to to over a fresh connection.
func smtpSend(t *testing.T, addr string, to ...string) error {
	t.Helper()
	host, port := split(t, addr)
	c, err := conn.DialSMTP(ctx, conn.Endpoint{Host: host, Port: port, Security: conn.None, Username: "alice", Password: "secret"})
	require.NoError(t, err)
	defer c.Close()
	return c.SendMail("a@b.c", to, strings.NewReader(rfc822))
}

func TestSMTPReplyAtRcpt(t *testing.T) {
	addr, in := testmail.StartSMTP(t)
	want := &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "No such user"}
	in.Reply("d@e.f", testmail.AtRcpt, want, false)
	for range 2 {
		var got *smtp.SMTPError
		require.ErrorAs(t, smtpSend(t, addr, "d@e.f"), &got)
		assert.Equal(t, want.Code, got.Code)
		assert.Equal(t, want.EnhancedCode, got.EnhancedCode)
		assert.Equal(t, want.Message, got.Message)
	}
	assert.Empty(t, in.Messages())
	require.NoError(t, smtpSend(t, addr, "g@h.i"), "other recipients are accepted")
	in.Reply("d@e.f", testmail.AtRcpt, nil, false)
	require.NoError(t, smtpSend(t, addr, "d@e.f"), "nil clears the reply")
	assert.Len(t, in.Messages(), 2)
}

func TestSMTPReplyAtDataOnce(t *testing.T) {
	addr, in := testmail.StartSMTP(t)
	in.Reply("d@e.f", testmail.AtData, &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "Try again later"}, true)
	var got *smtp.SMTPError
	require.ErrorAs(t, smtpSend(t, addr, "g@h.i", "d@e.f"), &got)
	assert.Equal(t, 451, got.Code)
	assert.Equal(t, smtp.EnhancedCode{4, 7, 1}, got.EnhancedCode)
	assert.Equal(t, "Try again later", got.Message)
	assert.Empty(t, in.Messages(), "a message refused at DATA is not recorded")
	require.NoError(t, smtpSend(t, addr, "g@h.i", "d@e.f"), "a one-time reply is used up")
	require.Len(t, in.Messages(), 1)
	assert.Equal(t, []string{"g@h.i", "d@e.f"}, in.Messages()[0].To)
}

func TestWithoutUIDPlusKeepsBodiesIntact(t *testing.T) {
	s := testmail.StartIMAP(t, testmail.WithoutUIDPlus())
	body := "From: a@b.c\r\nSubject: x\r\n\r\n* OK [COPYUID 1 2 3] and [APPENDUID 1 2] stay\r\n"
	_, err := s.User.Append("INBOX", bytesLiteral(body), &imap.AppendOptions{})
	require.NoError(t, err)
	c := dial(t, s)
	_, err = c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	sec := &imap.FetchItemBodySection{}
	msgs, err := c.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, body, string(msgs[0].FindBodySection(sec)))
}

func TestInboxMessagesAreCopies(t *testing.T) {
	addr, in := testmail.StartSMTP(t)
	host, port := split(t, addr)
	c, err := conn.DialSMTP(ctx, conn.Endpoint{Host: host, Port: port, Security: conn.None, Username: "alice", Password: "secret"})
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.SendMail("a@b.c", []string{"d@e.f"}, strings.NewReader(rfc822)))
	m := in.Messages()
	m[0].To[0] = "x"
	m[0].Data[0] = 'X'
	assert.Equal(t, "d@e.f", in.Messages()[0].To[0])
	assert.NotEqual(t, byte('X'), in.Messages()[0].Data[0])
}

func TestProtocolLookalikesInMessagesStayIntact(t *testing.T) {
	s := testmail.StartIMAP(t, testmail.WithoutIdle())
	body := "From: a@b.c\r\nSubject: * CAPABILITY IMAP4rev1 IDLE\r\n\r\n" +
		"a ID (\"name\" \"example\")\r\n" +
		"* CAPABILITY IMAP4rev1 IDLE\r\n" +
		"* OK [CAPABILITY IMAP4rev1 IDLE] hi\r\n" +
		"b APPEND INBOX {5}\r\n"
	c := dial(t, s)
	a := c.Append("INBOX", int64(len(body)), nil)
	_, err := a.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, a.Close())
	_, err = a.Wait()
	require.NoError(t, err)
	require.Len(t, s.IDs(), 1, "the ID line in the literal is not a command")

	_, err = c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	sec := &imap.FetchItemBodySection{}
	msgs, err := c.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{Envelope: true, BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, body, string(msgs[0].FindBodySection(sec)))
	assert.Equal(t, "* CAPABILITY IMAP4rev1 IDLE", msgs[0].Envelope.Subject)
}

func TestIDsAreCopies(t *testing.T) {
	s := testmail.StartIMAP(t)
	dial(t, s)
	s.IDs()[0]["name"] = "x"
	assert.Equal(t, "Godwit", s.IDs()[0]["name"])
}

func TestLiteralHeaderAcrossReadBuffer(t *testing.T) {
	s := testmail.StartIMAP(t)
	c := dial(t, s)
	body := "From: a@b.c\r\nSubject: x\r\n\r\na ID (\"name\" \"example\")\r\n"
	// The APPEND line is "T<n> APPEND <name> {<len>}\r\n"; sweeping the name length
	// puts the 4096-byte read-buffer boundary inside "{<len>}" for some of them.
	for n := 4060; n < 4096; n++ { // longer names go as literals, which imapserver caps at 4096
		name := strings.Repeat("m", n)
		require.NoError(t, c.Create(name, nil).Wait())
		a := c.Append(name, int64(len(body)), nil)
		_, err := a.Write([]byte(body))
		require.NoError(t, err)
		require.NoError(t, a.Close())
		_, err = a.Wait()
		require.NoError(t, err, n)
		_, err = c.Select(name, nil).Wait()
		require.NoError(t, err)
		sec := &imap.FetchItemBodySection{}
		msgs, err := c.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		require.Equal(t, body, string(msgs[0].FindBodySection(sec)), n)
		require.Len(t, s.IDs(), 1, n)
	}
}

func gmailFetch(t *testing.T, c *imapclient.Client, folder string) map[imap.UID]*imapclient.FetchMessageBuffer {
	t.Helper()
	_, err := c.Select(folder, nil).Wait()
	require.NoError(t, err)
	msgs, err := c.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{UID: true, Flags: true, GmailMsgID: true, GmailLabels: true}).Collect()
	require.NoError(t, err)
	out := map[imap.UID]*imapclient.FetchMessageBuffer{}
	for _, m := range msgs {
		out[m.UID] = m
	}
	return out
}

func uids(s imap.NumSet) []imap.UID {
	n, _ := s.(imap.UIDSet).Nums()
	return n
}

// A Gmail message is one object in several folders, each with its own UID:
// FETCH names it by X-GM-MSGID, flags are shared, and MOVE changes which
// folders hold it, answering with the UID it already has there.
func TestGmail(t *testing.T) {
	s := testmail.StartIMAP(t, testmail.Gmail())
	for _, f := range []string{"Work", "Later"} {
		require.NoError(t, s.User.Create(f, nil))
	}
	m := s.AddGmail(rfc822, nil, testmail.GmailAllMail, "Work", "INBOX")
	twin := s.AddGmail(rfc822, nil, "INBOX") // same content, another message
	assert.NotEqual(t, m.ID, twin.ID)
	assert.Equal(t, []string{"INBOX", "Work", testmail.GmailAllMail}, m.Folders())

	c := dial(t, s)
	assert.True(t, c.Caps().Has("X-GM-EXT-1"))
	list, err := c.List("", "*", nil).Collect()
	require.NoError(t, err)
	attrs := map[string][]imap.MailboxAttr{}
	for _, d := range list {
		attrs[d.Mailbox] = d.Attrs
	}
	assert.Contains(t, attrs[testmail.GmailAllMail], imap.MailboxAttrAll)
	assert.Contains(t, attrs[testmail.GmailTrash], imap.MailboxAttrTrash)

	inbox := gmailFetch(t, c, "INBOX")
	require.Len(t, inbox, 2)
	got := inbox[m.UID("INBOX")]
	assert.Equal(t, m.ID, got.GmailMsgID)
	assert.ElementsMatch(t, []string{`\Inbox`, "Work"}, got.GmailLabels)
	assert.Equal(t, twin.ID, inbox[twin.UID("INBOX")].GmailMsgID)

	require.NoError(t, c.Store(imap.UIDSetNum(m.UID("INBOX")), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagFlagged}}, nil).Close())
	assert.Contains(t, gmailFetch(t, c, "Work")[m.UID("Work")].Flags, imap.FlagFlagged, "flags are the message's")
	assert.NotContains(t, gmailFetch(t, c, "INBOX")[twin.UID("INBOX")].Flags, imap.FlagFlagged)

	all := m.UID(testmail.GmailAllMail)
	mv, err := c.Move(imap.UIDSetNum(m.UID("INBOX")), testmail.GmailAllMail).Wait() // archive
	require.NoError(t, err)
	assert.Equal(t, []imap.UID{all}, uids(mv.DestUIDs))
	assert.Equal(t, []string{"Work", testmail.GmailAllMail}, m.Folders())
	assert.Len(t, gmailFetch(t, c, "INBOX"), 1)
	assert.Len(t, gmailFetch(t, c, testmail.GmailAllMail), 1, "no second copy in All Mail")

	_, err = c.Select("Work", nil).Wait()
	require.NoError(t, err)
	mv, err = c.Move(imap.UIDSetNum(m.UID("Work")), "Later").Wait()
	require.NoError(t, err)
	assert.Equal(t, []imap.UID{m.UID("Later")}, uids(mv.DestUIDs))
	assert.Equal(t, []string{"Later", testmail.GmailAllMail}, m.Folders())
	later := gmailFetch(t, c, "Later")[m.UID("Later")]
	assert.Equal(t, m.ID, later.GmailMsgID)
	assert.Contains(t, later.Flags, imap.FlagFlagged)

	m.Unlabel("Later") // as the web app removes a label
	m.Label("INBOX")
	assert.Equal(t, []string{"INBOX", testmail.GmailAllMail}, m.Folders())
	assert.Empty(t, gmailFetch(t, c, "Later"))
	assert.Equal(t, m.ID, gmailFetch(t, c, "INBOX")[m.UID("INBOX")].GmailMsgID)
}

// Without Gmail, the server refuses X-GM-MSGID as any other server does.
func TestNotGmail(t *testing.T) {
	s := testmail.StartIMAP(t)
	seed(t, s, 1)
	c := dial(t, s)
	assert.False(t, c.Caps().Has("X-GM-EXT-1"))
	_, err := c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	_, err = c.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{UID: true, GmailMsgID: true}).Collect()
	assert.Error(t, err)
}

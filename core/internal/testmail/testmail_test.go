package testmail_test

import (
	"context"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/conn"
	"github.com/godwitmail/godwit/core/internal/testmail"
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

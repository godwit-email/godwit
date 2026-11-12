package core

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func BenchmarkSyncRound(b *testing.B) {
	for _, scale := range testmail.Scales() {
		for _, kind := range []string{"initial", "noop", "inbox_only"} {
			b.Run(scale.Name+"/"+kind, func(b *testing.B) {
				b.ReportAllocs()
				boxes := testmail.StartMailboxes(b, scale, testmail.DefaultSeed)
				var totalDown int64
				if kind == "initial" {
					root := b.TempDir()
					b.StopTimer()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						dir := filepath.Join(root, strconv.Itoa(i))
						s, ids := newBenchService(b, dir, boxes)
						b.StartTimer()
						down := runBenchRound(b, s, ids, boxes, false)
						b.StopTimer()
						totalDown += down
						if err := s.Close(); err != nil {
							b.Fatal(err)
						}
						_ = os.RemoveAll(dir)
					}
				} else {
					s, ids := newBenchService(b, b.TempDir(), boxes)
					b.Cleanup(func() { _ = s.Close() })
					runBenchRound(b, s, ids, boxes, false)
					inboxOnly := kind == "inbox_only"
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						totalDown += runBenchRound(b, s, ids, boxes, inboxOnly)
					}
				}
				b.ReportMetric(float64(totalDown)/float64(b.N), "bytes-down/op")
			})
		}
	}
}

func newBenchService(tb testing.TB, dir string, boxes []testmail.Mailbox) (*Service, []int64) {
	tb.Helper()
	keyring.MockInit()
	s, err := New(bg, Options{DataDir: dir, ImageClient: loopbackImageClient()})
	if err != nil {
		tb.Fatal(err)
	}
	ids := make([]int64, len(boxes))
	for i, mb := range boxes {
		ih, ip := splitBenchAddr(tb, mb.IMAP.Addr)
		sh, sp := splitBenchAddr(tb, mb.SMTPAddr)
		acc, err := s.AddAccount(bg, AccountInput{
			Email:     mb.Email,
			Username:  "alice",
			Password:  "secret",
			IsDefault: i == 0,
			Receiving: Server{Host: ih, Port: ip, Security: SecurityNone},
			Sending:   Server{Host: sh, Port: sp, Security: SecurityNone},
		})
		if err != nil {
			_ = s.Close()
			tb.Fatal(err)
		}
		ids[i] = acc.ID
	}
	return s, ids
}

func runBenchRound(tb testing.TB, s *Service, ids []int64, boxes []testmail.Mailbox, inboxOnly bool) int64 {
	tb.Helper()
	before := totalBytesSent(boxes)
	for _, id := range ids {
		var only []backend.FolderRef
		if inboxOnly {
			only = s.inboxOnly(bg, id)
		}
		if err := s.syncNow(bg, id, false, only); err != nil {
			tb.Fatal(err)
		}
	}
	return totalBytesSent(boxes) - before
}

func totalBytesSent(boxes []testmail.Mailbox) int64 {
	var sum int64
	for _, mb := range boxes {
		sum += mb.IMAP.BytesSent()
	}
	return sum
}

func splitBenchAddr(tb testing.TB, addr string) (string, int) {
	tb.Helper()
	i := strings.LastIndex(addr, ":")
	p, err := strconv.Atoi(addr[i+1:])
	if err != nil {
		tb.Fatal(err)
	}
	return addr[:i], p
}

func TestSyncRoundBytesDelta(t *testing.T) {
	boxes := testmail.StartMailboxes(t, testmail.ScaleSmall, testmail.DefaultSeed)
	s, ids := newBenchService(t, t.TempDir(), boxes)
	t.Cleanup(func() { _ = s.Close() })

	// AddAccount already talked to the IMAP server; the initial round's delta
	// must exclude that setup traffic and download every seeded message.
	setupSent := totalBytesSent(boxes)
	require.Greater(t, setupSent, int64(0))

	initialDown := runBenchRound(t, s, ids, boxes, false)
	assert.Equal(t, totalBytesSent(boxes)-setupSent, initialDown)

	rows, err := s.db.Query(bg, "SELECT count(*) FROM messages")
	require.NoError(t, err)
	assert.Equal(t, int64(testmail.ScaleSmall.Accounts*testmail.ScaleSmall.Messages), store.Int(rows.Values[0][0]))

	noopDown := runBenchRound(t, s, ids, boxes, false)
	noopDownAgain := runBenchRound(t, s, ids, boxes, false)
	assert.Greater(t, noopDown, int64(0))
	assert.Less(t, noopDown, initialDown, "noop delta must not accumulate initial sync bytes")
	assert.Equal(t, noopDown, noopDownAgain, "repeated noop rounds have constant per-round delta")

	inboxDown := runBenchRound(t, s, ids, boxes, true)
	assert.Greater(t, inboxDown, int64(0))
	assert.Less(t, inboxDown, noopDown, "inbox-only round checks only INBOX")

	// Confirm inboxOnly actually skips non-inbox folders while fetching INBOX.
	_, err = boxes[0].IMAP.User.Append("Archive", bytes.NewReader([]byte(
		eml("bench-ar@x", "Bob <bob@example.org>", boxes[0].Email, "later archive", "Thu, 15 Oct 2026 09:00:00 +0000", "skip me"),
	)), &imap.AppendOptions{})
	require.NoError(t, err)
	_, err = boxes[0].IMAP.User.Append("INBOX", bytes.NewReader([]byte(
		eml("bench-in@x", "Bob <bob@example.org>", boxes[0].Email, "later inbox", "Thu, 15 Oct 2026 09:01:00 +0000", "fetch me"),
	)), &imap.AppendOptions{})
	require.NoError(t, err)

	runBenchRound(t, s, ids, boxes, true)

	gotIn, err := s.db.Query(bg, "SELECT count(*) FROM messages WHERE message_id = 'bench-in@x'")
	require.NoError(t, err)
	assert.Equal(t, int64(1), store.Int(gotIn.Values[0][0]))
	gotAr, err := s.db.Query(bg, "SELECT count(*) FROM messages WHERE message_id = 'bench-ar@x'")
	require.NoError(t, err)
	assert.Equal(t, int64(0), store.Int(gotAr.Values[0][0]))
}

package testmail

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// DefaultSeed is the fixed PRNG seed benchmarks use so every run builds the same mailboxes.
const DefaultSeed int64 = 716

// Scale sizes a seeded benchmark fixture: Accounts mailboxes, each with
// Folders total folders and Messages total messages across those folders.
type Scale struct {
	Name     string
	Accounts int
	Folders  int
	Messages int
}

var (
	ScaleSmall  = Scale{Name: "small", Accounts: 1, Folders: 5, Messages: 50}
	ScaleMedium = Scale{Name: "medium", Accounts: 2, Folders: 12, Messages: 200}
	ScaleLarge  = Scale{Name: "large", Accounts: 3, Folders: 24, Messages: 400}
)

// Scales returns the three benchmark tiers in ascending order.
func Scales() []Scale {
	return []Scale{ScaleSmall, ScaleMedium, ScaleLarge}
}

// Mailbox is one seeded account's in-memory IMAP and SMTP server pair.
type Mailbox struct {
	Email    string
	IMAP     *IMAP
	SMTPAddr string
	SMTP     *Inbox
}

var (
	seedSenders = []string{
		"Bob <bob@example.org>",
		"Carol <carol@example.org>",
		"Dan <dan@example.net>",
		"Erin <erin@example.net>",
		"Frank <frank@example.org>",
		"Notifications <noreply@builds.example.org>",
	}
	seedSubjects = []string{
		"Quarterly roadmap notes",
		"Release checklist for next week",
		"Build report and test timings",
		"Design review follow-up",
		"Invoice #1042 for October",
		"Customer migration schedule",
		"Weekly sync summary",
		"Storage quota warning",
	}
	seedLines = []string{
		"Here is the latest update from today's check-in.",
		"All unit and integration suites passed on clean checkout.",
		"Please take a look at section two before Thursday morning.",
		"We kept the schema unchanged and trimmed the extra round-trip.",
		"Attached are the numbers from the local test run for comparison.",
		"Let me know if we should adjust the rollout window.",
	}
)

// StartMailboxes starts scale.Accounts in-memory IMAP and SMTP servers and
// populates each IMAP server deterministically from seed.
func StartMailboxes(tb testing.TB, scale Scale, seed int64, opts ...Option) []Mailbox {
	tb.Helper()
	rng := rand.New(rand.NewSource(seed))
	out := make([]Mailbox, scale.Accounts)
	for i := range scale.Accounts {
		subSeed := rng.Int63()
		email := fmt.Sprintf("alice%d@example.com", i+1)
		im := StartIMAP(tb, opts...)
		smtpAddr, smtpIn := StartSMTP(tb)
		PopulateMailbox(tb, im, email, subSeed, scale.Folders, scale.Messages)
		out[i] = Mailbox{Email: email, IMAP: im, SMTPAddr: smtpAddr, SMTP: smtpIn}
	}
	return out
}

// PopulateMailbox creates extra project folders up to folders (>= 4) on im and
// appends messages deterministic RFC 5322 messages driven by seed.
func PopulateMailbox(tb testing.TB, im *IMAP, email string, seed int64, folders, messages int) {
	tb.Helper()
	if folders < 4 {
		tb.Fatalf("testmail: folders (%d) must be at least 4", folders)
	}
	extra := make([]string, 0, folders-4)
	for i := 1; i <= folders-4; i++ {
		name := fmt.Sprintf("Projects/%02d", i)
		if err := im.User.Create(name, nil); err != nil {
			tb.Fatal(err)
		}
		extra = append(extra, name)
	}

	type threadState struct {
		subject string
		ids     []string
	}
	var threads []threadState
	rng := rand.New(rand.NewSource(seed))
	ts := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	for i := range messages {
		ts = ts.Add(time.Duration(30+rng.Intn(900)) * time.Second)
		folder := pickFolder(rng, i, extra)
		peer := seedSenders[rng.Intn(len(seedSenders))]
		from, to := peer, email
		if folder == "Sent" {
			from, to = email, peer
		}

		mid := fmt.Sprintf("seed-%016x-%d@example.com", uint64(rng.Int63()), i+1)
		var subject, inReplyTo, refs string
		if len(threads) > 0 && rng.Intn(100) < 45 {
			idx := rng.Intn(len(threads))
			th := &threads[idx]
			subject = "Re: " + th.subject
			inReplyTo = "<" + th.ids[len(th.ids)-1] + ">"
			refs = "<" + strings.Join(th.ids, "> <") + ">"
			if len(th.ids) < 6 {
				th.ids = append(th.ids, mid)
			}
		} else {
			subject = fmt.Sprintf("%s (%d)", seedSubjects[rng.Intn(len(seedSubjects))], i+1)
			threads = append(threads, threadState{subject: subject, ids: []string{mid}})
		}

		raw := buildSeedMessage(rng, mid, from, to, subject, inReplyTo, refs, ts)
		flags := pickFlags(rng, folder)
		if _, err := im.User.Append(folder, bytes.NewReader(raw), &imap.AppendOptions{
			Time:  ts,
			Flags: flags,
		}); err != nil {
			tb.Fatal(err)
		}
	}
}

func pickFolder(rng *rand.Rand, index int, extra []string) string {
	if index == 0 {
		return "INBOX"
	}
	roll := rng.Intn(100)
	switch {
	case roll < 30:
		return "INBOX"
	case roll < 45:
		return "Sent"
	case roll < 50:
		return "Trash"
	case roll < 65 || len(extra) == 0:
		return "Archive"
	default:
		return extra[rng.Intn(len(extra))]
	}
}

func pickFlags(rng *rand.Rand, folder string) []imap.Flag {
	if folder == "Sent" {
		return []imap.Flag{imap.FlagSeen}
	}
	var flags []imap.Flag
	if rng.Intn(100) < 75 {
		flags = append(flags, imap.FlagSeen)
	}
	if rng.Intn(100) < 10 {
		flags = append(flags, imap.FlagFlagged)
	}
	return flags
}

func buildSeedMessage(rng *rand.Rand, mid, from, to, subject, inReplyTo, refs string, ts time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Message-ID: <%s>\r\n", mid)
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	if rng.Intn(100) < 20 {
		fmt.Fprintf(&b, "Cc: %s\r\n", seedSenders[rng.Intn(len(seedSenders))])
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", ts.Format(time.RFC1123Z))
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", inReplyTo)
		fmt.Fprintf(&b, "References: %s\r\n", refs)
	}
	b.WriteString("MIME-Version: 1.0\r\n")

	nLines := 2 + rng.Intn(4)
	lines := make([]string, nLines)
	for i := range lines {
		lines[i] = seedLines[rng.Intn(len(seedLines))]
	}
	text := strings.Join(lines, "\r\n")

	kind := rng.Intn(100)
	switch {
	case kind < 65:
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(text)
		b.WriteString("\r\n")
	case kind < 90:
		boundary := fmt.Sprintf("alt-%s", mid)
		fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
		fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, text)
		fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>%s</p>\r\n", boundary, strings.Join(lines, "</p><p>"))
		fmt.Fprintf(&b, "--%s--\r\n", boundary)
	default:
		boundary := fmt.Sprintf("mix-%s", mid)
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
		fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, text)
		fmt.Fprintf(&b, "--%s\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"note.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n", boundary)
		fmt.Fprintf(&b, "--%s--\r\n", boundary)
	}
	return b.Bytes()
}

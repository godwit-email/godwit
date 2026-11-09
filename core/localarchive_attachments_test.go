package core_test

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const pixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// archiveMailWithFiles is an invoice with a cid logo in its HTML and the
// testdata pdf and docx attached.
func archiveMailWithFiles(t *testing.T) (raw, pdf, docx []byte) {
	t.Helper()
	pdf, err := os.ReadFile("testdata/attachment.pdf")
	require.NoError(t, err)
	docx, err = os.ReadFile("testdata/attachment.docx")
	require.NoError(t, err)
	b64 := func(d []byte) string {
		s := base64.StdEncoding.EncodeToString(d)
		var b strings.Builder
		for len(s) > 76 {
			b.WriteString(s[:76] + "\r\n")
			s = s[76:]
		}
		return b.String() + s
	}
	raw = []byte("Message-ID: <inv@example.com>\r\nFrom: Bob <bob@example.com>\r\nTo: me@example.com\r\nSubject: invoice\r\n" +
		"Date: Mon, 05 Oct 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=MX\r\n\r\n" +
		"--MX\r\nContent-Type: multipart/related; boundary=RL\r\n\r\n" +
		"--RL\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>see</p><img src=\"cid:logo@x\">\r\n" +
		"--RL\r\nContent-Type: image/png\r\nContent-ID: <logo@x>\r\nContent-Disposition: inline; filename=\"logo.png\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + pixelPNG + "\r\n" +
		"--RL--\r\n" +
		"--MX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"a.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(pdf) + "\r\n" +
		"--MX\r\nContent-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document\r\nContent-Disposition: attachment; filename=\"a.docx\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(docx) + "\r\n" +
		"--MX--\r\n")
	return raw, pdf, docx
}

// A local archive's attachments come from its original, offline, and outside
// the attachment cache: eviction or clearing the cache never loses them.
func TestLocalArchiveAttachmentsReadFromOriginal(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	raw, pdf, docx := archiveMailWithFiles(t)
	m, err := s.GetMessage(ctx, importOne(t, s, acc, inbox, raw).ID, false)
	require.NoError(t, err)
	want := map[string][]byte{"a.pdf": pdf, "a.docx": docx}

	read := func() {
		t.Helper()
		n := 0
		for _, a := range m.Attachments {
			if want[a.Filename] == nil {
				continue
			}
			n++
			p, info, err := s.GetAttachment(ctx, a.ID)
			require.NoError(t, err) // a dial would fail: the archive has no server
			require.Equal(t, a.Filename, info.Filename)
			got, err := os.ReadFile(p)
			require.NoError(t, err)
			require.Equal(t, want[a.Filename], got, a.Filename)
		}
		require.Equal(t, 2, n)
	}
	read()
	used, err := s.AttachmentCacheUsage(ctx, acc.ID)
	require.NoError(t, err)
	require.Zero(t, used, "not in the attachment cache")

	require.NoError(t, s.EvictForTest(ctx))
	read()
	require.NoError(t, s.ClearAttachmentCache(ctx, acc.ID))
	read()
}

// The reader shows a local archive's cid images, read from the original.
func TestLocalArchiveInlineImageShows(t *testing.T) {
	s := newTestService(t)
	acc, inbox := newArchive(t, s)
	raw, _, _ := archiveMailWithFiles(t)
	m, err := s.GetMessage(ctx, importOne(t, s, acc, inbox, raw).ID, false)
	require.NoError(t, err)
	require.Contains(t, m.HTML, "data:image/png;base64,")
}

// Opening an attachment while its message is deleted for good must not write
// the part back after the delete. Racy by nature: it checks over several rounds.
func TestLocalArchiveAttachmentRacingDeleteLeavesNoFiles(t *testing.T) {
	dir := t.TempDir()
	s := newServiceAt(t, dir)
	acc, inbox := newArchive(t, s)
	trash, _ := folderNamed(t, s, acc.ID, "Trash")
	raw, _, _ := archiveMailWithFiles(t)
	for range 20 {
		m, err := s.GetMessage(ctx, importOne(t, s, acc, inbox, raw).ID, false)
		require.NoError(t, err)
		require.NoError(t, s.Delete(ctx, []int64{m.ID}))
		done := make(chan error, 1)
		go func() { _, err := s.EmptyFolder(ctx, trash.ID); done <- err }()
		for _, a := range m.Attachments {
			s.GetAttachment(ctx, a.ID) // fails once the delete went first
		}
		require.NoError(t, <-done)
		require.Empty(t, archiveFiles(t, dir, acc.ID), "nothing written back")
	}
}

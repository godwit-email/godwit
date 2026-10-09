package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inlineAndAttached has an HTML body showing a cid: image, and a PDF.
const inlineAndAttached = "Message-ID: <ia@x>\r\nFrom: Bob <bob@example.com>\r\nTo: alice@example.com\r\nReply-To: desk@example.com\r\n" +
	"Subject: chart\r\nDate: Mon, 05 Oct 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=MX\r\n\r\n" +
	"--MX\r\nContent-Type: multipart/related; boundary=RX\r\n\r\n" +
	"--RX\r\nContent-Type: multipart/alternative; boundary=AX\r\n\r\n" +
	"--AX\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nsee the chart\r\n" +
	"--AX\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>see <img src=\"cid:chart1\"></p>\r\n--AX--\r\n" +
	"--RX\r\nContent-Type: image/png\r\nContent-ID: <chart1>\r\nContent-Disposition: inline; filename=\"c.png\"\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n--RX--\r\n" +
	"--MX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"r.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--MX--\r\n"

// GetMessageText reads stored text and metadata only: no IMAP traffic, no
// attachment or inline image in the cache.
func TestMCPReadDownloadsNothing(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", inlineAndAttached)
	e.sync(t)
	id := e.idOf(t, "ia@x")
	require.Equal(t, 1, e.count(t, "SELECT count(*) FROM attachments WHERE content_id != '' AND inline = 1"), "the inline image is known")
	cache := filepath.Join(e.dir, "cache")
	files := func() int {
		n := 0
		filepath.WalkDir(cache, func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
		return n
	}

	before := e.im.BytesSent()
	m, err := e.s.GetMessageText(bg, id)
	require.NoError(t, err)
	assert.Equal(t, before, e.im.BytesSent(), "no IMAP traffic")
	assert.Zero(t, files(), "nothing cached")
	assert.Zero(t, e.count(t, "SELECT count(*) FROM attachments WHERE cache_path IS NOT NULL"))

	assert.Equal(t, id, m.ID)
	assert.Equal(t, e.acc.ID, m.AccountID)
	assert.Equal(t, "chart", m.Subject)
	assert.Equal(t, "desk@example.com", m.ReplyTo)
	assert.Contains(t, m.Text, "see the chart")
	assert.NotContains(t, m.Text, "<img")
	assert.False(t, m.Truncated)
	names := map[string]bool{}
	for _, a := range m.Attachments {
		names[a.Filename] = a.Inline
	}
	assert.Equal(t, map[string]bool{"c.png": true, "r.pdf": false}, names)

	// The same message through GetMessage does download its inline image,
	// so the checks above would have seen it.
	_, err = e.s.GetMessage(bg, id, false)
	require.NoError(t, err)
	assert.Greater(t, e.im.BytesSent(), before)
	assert.Equal(t, 1, files())

	_, err = e.s.GetMessageText(bg, 99999)
	assert.Error(t, err)
}

func TestAuditExternal(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.s.AuditExternal(bg, "local_api", "Send", 42))
	require.NoError(t, e.s.AuditExternal(bg, "local_mcp", "SaveDraft", 0))
	assert.Error(t, e.s.AuditExternal(bg, "plugin", "Send", 1), "only external sources")
	rows := e.rows(t, "SELECT source, tool, target_id, decision FROM audit_log ORDER BY id")
	require.Len(t, rows, 2)
	assert.Equal(t, []any{"local_api", "Send", int64(42), "allow"}, rows[0])
	assert.Equal(t, []any{"local_mcp", "SaveDraft", nil, "allow"}, rows[1])
}

package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// invoiceMail is a message from billing@ with invoice.pdf attached (holding
// body) and a logo shown inline in its HTML body.
func invoiceMail(id, subject, body string) string {
	return "Message-ID: <" + id + ">\r\nFrom: Stripe <billing@stripe.example>\r\nTo: alice@example.com\r\nSubject: " + subject + "\r\n" +
		"Date: " + day + "\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n" +
		"--XX\r\nContent-Type: multipart/related; boundary=YY\r\n\r\n" +
		"--YY\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Hi <img src=\"cid:logo@x\"></p>\r\n" +
		"--YY\r\nContent-Type: image/png\r\nContent-ID: <logo@x>\r\nContent-Disposition: inline; filename=\"logo.png\"\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n" +
		"--YY--\r\n" +
		"--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"invoice.pdf\"\r\n\r\n" + body + "\r\n" +
		"--XX--\r\n"
}

// fileRule writes mail from billing@ to dir, one subfolder per message.
func fileRule(dir string, acts ...string) RuleDoc {
	d := rule()
	for _, k := range acts {
		d.Actions = append(d.Actions, RuleAction{Kind: k, Dir: dir, Subfolder: true, Naming: "{date} {subject}"})
	}
	return d
}

// tree lists the files under dir as "path: content", relative and sorted.
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel)+": "+string(b))
		return err
	}))
	return out
}

// Two messages with the same subject and an invoice.pdf each get a folder
// each, beside a folder of that name the user already had, which is left
// as it was; the inline logo is not saved.
func TestRuleSavesFilesInSubfolders(t *testing.T) {
	e := newRulesEnv(t)
	dest := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dest, "2026-10-05 Invoice"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "2026-10-05 Invoice", "invoice.pdf"), []byte("mine"), 0o644))
	e.saveRule(t, fileRule(dest, ActionAttachments, ActionEML), true)
	e.put(t, "INBOX", invoiceMail("a@x", "Invoice", "first"))
	e.put(t, "INBOX", invoiceMail("b@x", "Invoice", "second"))
	e.sync(t)
	e.waitRuns(t, 2)

	files := tree(t, dest)
	assert.Contains(t, files, "2026-10-05 Invoice/invoice.pdf: mine")
	assert.Contains(t, files, "2026-10-05 Invoice (2)/invoice.pdf: first")
	assert.Contains(t, files, "2026-10-05 Invoice (3)/invoice.pdf: second")
	assert.Len(t, files, 5, "two .eml beside the invoices: %q", files)
	raw, err := os.ReadFile(filepath.Join(dest, "2026-10-05 Invoice (2)", "2026-10-05 Invoice.eml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Message-ID: <a@x>")
	run := e.runOf(t, "a@x")
	assert.Equal(t, []string{"attachments done", "eml done"}, e.results(t, run))
	want, err := json.Marshal([]string{filepath.Join(dest, "2026-10-05 Invoice (2)", "invoice.pdf")}) // escapes Windows backslashes
	require.NoError(t, err)
	assert.Equal(t, [][]any{{string(want)}},
		e.rows(t, "SELECT result FROM rule_action_results WHERE run_id = ? AND idx = 0", run))
}

// A subfolder level that is a symlink, or a file, inside the destination
// fails the action: nothing is written through it, outside the destination.
func TestRuleFilesRefuseALinkedLevel(t *testing.T) {
	e := newRulesEnv(t)
	dest, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "Stripe")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dest, "Bob"), []byte("x"), 0o644))
	d := rule(RuleAction{Kind: ActionEML, Dir: dest, Subfolder: true, Naming: "{from}/{subject}"})
	d.Conditions = []RuleCondition{}
	e.saveRule(t, d, true)
	e.put(t, "INBOX", invoiceMail("a@x", "Invoice", "first"))
	e.put(t, "INBOX", eml("b@x", "Bob <bob@example.com>", "alice@example.com", "Hi", day, "b"))
	e.sync(t)
	e.waitRuns(t, 2)

	assert.Empty(t, tree(t, outside))
	assert.Equal(t, [][]any{{RuleFailUnsafeFolder}, {RuleFailUnsafeFolder}}, e.rows(t, "SELECT result FROM rule_action_results ORDER BY run_id"))
}

// A crash after a file was written resumes into the same folder and keeps
// that file rather than writing a numbered copy.
func TestRuleFilesResumeAfterCrash(t *testing.T) {
	e := newRulesEnv(t)
	dest := t.TempDir()
	e.saveRule(t, fileRule(dest, ActionAttachments, ActionEML), true)
	stopped := stopRulesAt(t, "rules.file")
	e.put(t, "INBOX", invoiceMail("a@x", "Invoice", "first"))
	e.sync(t)
	<-stopped
	rulesHook = nil
	e.reopen(t)
	e.waitRuns(t, 1)
	files := tree(t, dest)
	assert.Contains(t, files, "2026-10-05 Invoice/invoice.pdf: first")
	assert.Len(t, files, 2, "%q", files)
}

// A destination that is not there fails the action, creates nothing, and
// the actions after it do not run; a retry once it is there finishes.
func TestRuleFilesMissingDestination(t *testing.T) {
	e := newRulesEnv(t)
	dest := filepath.Join(t.TempDir(), "gone")
	id := e.saveRule(t, fileRule(dest, ActionEML, ActionAttachments), true)
	e.put(t, "INBOX", invoiceMail("a@x", "Invoice", "first"))
	e.sync(t)
	e.waitRuns(t, 1)
	run := e.runOf(t, "a@x")
	assert.Equal(t, []string{"eml failed", "attachments not_run"}, e.results(t, run))
	assert.NoDirExists(t, dest)
	hits, err := e.s.RuleHits(bg, id)
	require.NoError(t, err)
	assert.Equal(t, RuleFailNoDestination, hits[0].Actions[0].Failure)

	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, e.s.RetryRuleRun(bg, run))
	assert.Equal(t, []string{"eml done", "attachments done"}, e.results(t, run))
	assert.Len(t, tree(t, dest), 2)
}

// Every file action of a rule writing to one destination shares its naming and subfolder.
func TestRuleFilesValidation(t *testing.T) {
	e := newRulesEnv(t)
	dest := t.TempDir()
	doc := func(acts ...RuleAction) RuleDoc {
		return RuleDoc{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll, Actions: acts}
	}
	for name, d := range map[string]RuleDoc{
		"relative dir":      fileRule("docs", ActionEML),
		"literal naming":    doc(RuleAction{Kind: ActionEML, Dir: dest, Subfolder: true, Naming: "mail"}),
		"dot-dot naming":    doc(RuleAction{Kind: ActionEML, Dir: dest, Subfolder: true, Naming: "../{date}"}),
		"naming differs":    doc(RuleAction{Kind: ActionEML, Dir: dest, Subfolder: true, Naming: "{date}"}, RuleAction{Kind: ActionPDF, Dir: dest, Subfolder: true, Naming: "{subject}"}),
		"subfolder differs": doc(RuleAction{Kind: ActionEML, Dir: dest, Subfolder: true, Naming: "{date}"}, RuleAction{Kind: ActionPDF, Dir: dest, Naming: "{date}"}),
	} {
		_, err := e.s.SaveRule(bg, Rule{Doc: d})
		assert.ErrorIs(t, err, ErrInvalidRule, name)
	}
	_, err := e.s.SaveRule(bg, Rule{Doc: fileRule(dest, ActionEML, ActionPDF, ActionAttachments)})
	assert.NoError(t, err)
}

// A PDF of a Chinese message is rendered from a UTF-8 document holding its
// text, and named after its subfolder.
func TestRuleExportsPDF(t *testing.T) {
	got := make(chan string, 1)
	e := newSyncEnvWith(t, Options{PrintPDF: func(_ context.Context, html string) ([]byte, error) {
		got <- html
		return []byte("%PDF-1.4"), nil
	}})
	e.newMail(t, "old@x", "before")
	e.sync(t)
	dest := t.TempDir()
	e.saveRule(t, RuleDoc{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll,
		Actions: []RuleAction{{Kind: ActionPDF, Dir: dest, Subfolder: true, Naming: "{date} {subject}"}}}, true)
	subject := "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte("发票已开具")) + "?="
	e.put(t, "INBOX", eml("cn@x", "=?UTF-8?B?"+base64.StdEncoding.EncodeToString([]byte("王小明"))+"?= <wang@example.cn>", "alice@example.com", subject, day, "您好，附件是本月的发票。"))
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"2026-10-05 发票已开具/2026-10-05 发票已开具.pdf: %PDF-1.4"}, tree(t, dest))
	html := <-got
	assert.Contains(t, html, `<meta charset="utf-8">`)
	assert.Contains(t, html, "发票已开具")
	assert.Contains(t, html, "您好，附件是本月的发票。")
	assert.Contains(t, html, "王小明")
}

// Without a renderer (headless) the PDF action fails and says why.
func TestRulePDFUnavailable(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, fileRule(t.TempDir(), ActionPDF), true)
	e.put(t, "INBOX", invoiceMail("a@x", "Invoice", "first"))
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, [][]any{{"failed", RuleFailNoPDF}}, e.rows(t, "SELECT state, result FROM rule_action_results"))
}

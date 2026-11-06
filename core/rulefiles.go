package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/store"
)

// Rules that write files (ADR 0030, 写文件). A 命名模板 is literal text and
// {id} tokens; folder levels come only from the template's "/", since the
// values, which come from the mail, lose their separators first.

// NamingTokens are the token ids a 命名模板 may use, in the order the token field offers them.
var NamingTokens = []string{"date", "subject", "from", "domain", "year", "month", "account"}

// NamingPresets are the templates the editor offers before 自定义; the first is the default.
var NamingPresets = []string{"{date} {subject}", "{from}/{date} {subject}", "{year}/{month}/{date} {subject}"}

const (
	maxNameValue = 80  // runes of one token's value
	maxNameBytes = 200 // bytes of one level: room for " (2).pdf" under the usual 255
)

var namingToken = regexp.MustCompile(`\{([a-z]+)\}`)

// FillNaming fills tpl with vals (by token id) and returns its folder levels, each a safe
// file name. It refuses a template with no token (every message would land
// in one folder) and an empty, "." or ".." level.
func FillNaming(tpl string, vals map[string]string) ([]string, error) {
	known := false
	filled := namingToken.ReplaceAllStringFunc(tpl, func(m string) string {
		id := m[1 : len(m)-1]
		if !slices.Contains(NamingTokens, id) {
			return m
		}
		known = true
		fallback := "-"
		if id == "subject" {
			fallback = "(no subject)"
		}
		return nameValue(vals[id], fallback)
	})
	if !known {
		return nil, invalidRule("naming %q has no token", tpl)
	}
	levels := strings.Split(filled, "/")
	for i, l := range levels {
		if t := strings.TrimSpace(l); t == "" || t == "." || t == ".." {
			return nil, invalidRule("naming %q has an empty, . or .. level", tpl)
		}
		levels[i] = capBytes(FileName(l, "-"), maxNameBytes)
	}
	return levels, nil
}

// nameValue is a token's value made one safe piece of a name: separators and
// control characters become -, outer spaces and dots go, at most maxNameValue runes.
func nameValue(v, fallback string) string {
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, v)
	v = strings.Trim(v, ". \t")
	if r := []rune(v); len(r) > maxNameValue {
		v = strings.TrimRight(string(r[:maxNameValue]), ". ")
	}
	return cmp.Or(v, fallback)
}

// capBytes cuts s to at most n bytes on a rune boundary.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimRight(s[:n], ". ")
}

// numberedName is name's n-th candidate: "invoice.pdf", "invoice (2).pdf", …
func numberedName(name string, n int) string {
	if n <= 1 {
		return name
	}
	stem, ext := name, ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		stem, ext = name[:i], name[i:]
	}
	return stem + " (" + strconv.Itoa(n) + ")" + ext
}

// FileName makes s a file name: separators and control characters become
// _, leading and trailing dots and spaces go, and a name Windows reserves
// (CON, COM1.txt…) gets a _ in front. Empty, it is fallback.
func FileName(s, fallback string) string {
	s = strings.Trim(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`/\:*?"<>|`, r) { // Cf: U+202E turns "fdp.exe" into "exe.pdf"
			return '_'
		}
		return r
	}, s), ". ")
	stem, _, _ := strings.Cut(strings.ToUpper(s), ".")
	if reservedNames[stem] || len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		s = "_" + s
	}
	return cmp.Or(s, fallback)
}

var reservedNames = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

// RuleFileActions are the actions that write files.
var RuleFileActions = []string{ActionPDF, ActionEML, ActionAttachments, ActionKeyPoints}

// namingSample fills a template to check it at save, as any message would.
var namingSample = map[string]string{"date": "2026-11-03", "subject": "s", "from": "f", "domain": "d", "year": "2026", "month": "11", "account": "a"}

// validFileAction checks a's destination and naming, and that the file
// actions of acts writing to its destination name and group as it does.
func validFileAction(acts []RuleAction, a RuleAction) error {
	if !filepath.IsAbs(a.Dir) || utf8.RuneCountInString(a.Dir) > MaxRuleValue {
		return invalidRule("dir %q", a.Dir)
	}
	if _, err := FillNaming(a.Naming, namingSample); err != nil {
		return err
	}
	for _, b := range acts {
		if slices.Contains(RuleFileActions, b.Kind) && b.Dir == a.Dir && (b.Naming != a.Naming || b.Subfolder != a.Subfolder) {
			return invalidRule("file actions to %q name their files differently", a.Dir)
		}
	}
	return nil
}

// ruleFiles runs file action a on the message and returns the paths it
// wrote; failure, a RuleFail id, is why it could not. An error is the
// database's. Files are only ever created, never replaced.
func (s *Service) ruleFiles(ctx context.Context, runID int64, a RuleAction, msgID int64) (paths []string, failure string, err error) {
	if st, err := os.Stat(a.Dir); err != nil || !st.IsDir() {
		return nil, RuleFailNoDestination, nil // made by the user, never by a rule
	}
	if a.Kind == ActionPDF && s.opts.PrintPDF == nil {
		return nil, RuleFailNoPDF, nil
	}
	vals, err := s.namingValues(ctx, msgID)
	if err != nil {
		return nil, "", err
	}
	levels, err := FillNaming(a.Naming, vals)
	if err != nil {
		return nil, RuleFailUnknown, nil // SaveRule checked it
	}
	name := levels[len(levels)-1]
	dir := a.Dir
	if a.Subfolder {
		if dir, failure, err = s.ruleDir(ctx, runID, a.Dir, levels); dir == "" {
			return nil, failure, err
		}
	}
	write := func(name string, data []byte) string {
		p, werr := createNew(dir, name, data)
		if werr != nil {
			return RuleFailWrite
		}
		paths = append(paths, p)
		return ""
	}
	switch a.Kind {
	case ActionEML:
		raw, err := s.GetRawMessage(ctx, msgID)
		if f := fetchFailure(err); f != "" {
			return nil, f, nil
		}
		failure = write(name+".eml", raw)
	case ActionPDF:
		doc, err := s.mailDocument(ctx, msgID)
		if err != nil {
			return nil, "", err
		}
		pdf, err := s.opts.PrintPDF(ctx, doc)
		if err != nil {
			return nil, RuleFailWrite, nil
		}
		failure = write(name+".pdf", pdf)
	case ActionKeyPoints:
		points, f, err := s.ruleKeyPoints(ctx, msgID)
		if err != nil || f != "" {
			return nil, f, err
		}
		failure = write(name+".md", []byte(points))
	case ActionAttachments:
		// Images the body shows inline are part of it, not attachments.
		rows, err := s.db.Query(ctx, `SELECT id, filename FROM attachments WHERE message_id = ?
			AND NOT (inline = 1 AND content_type LIKE 'image/%') ORDER BY id`, msgID)
		if err != nil {
			return nil, "", err
		}
		fetcher := s.newAttachmentFetcher(ctx)
		defer fetcher.close()
		for _, v := range rows.Values {
			p, _, err := fetcher.get(store.Int(v[0]))
			if f := fetchFailure(err); f != "" {
				return paths, f, nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return paths, RuleFailUnavailable, nil
			}
			if failure = write(FileName(store.Str(v[1]), "attachment"), data); failure != "" {
				break
			}
			if rulesHook != nil && rulesHook("rules.file") != nil {
				return paths, "", errStopConsumer // tests: a crash after a file was written
			}
		}
	}
	return paths, failure, nil
}

// fetchFailure is why reading the mail from the server failed, "" if it did not.
func fetchFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrTooLarge):
		return RuleFailTooLarge
	}
	return RuleFailUnavailable
}

// namingValues are the message's token values, unsanitized; the date is the
// mail's, in local time.
func (s *Service) namingValues(ctx context.Context, msgID int64) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT m.subject, m.from_addr, m.date, a.email FROM messages m JOIN accounts a ON a.id = m.account_id WHERE m.id = ?`, msgID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("message %d not found", msgID)
	}
	v := rows.Values[0]
	from := store.Str(v[1])
	domain := ""
	if a, err := mail.ParseAddress(from); err == nil {
		_, domain, _ = strings.Cut(a.Address, "@")
	}
	d := time.Unix(store.Int(v[2]), 0).Local()
	return map[string]string{"date": d.Format(time.DateOnly), "year": d.Format("2006"), "month": d.Format("01"),
		"subject": store.Str(v[0]), "from": displayName(from), "domain": domain, "account": store.Str(v[3])}, nil
}

// ruleDir is the run's subfolder in dest: the one it chose before, or a new
// one named by levels, numbered past any that exists. The choice is kept in
// the run before the folder is made, so a retry finds it.
func (s *Service) ruleDir(ctx context.Context, runID int64, dest string, levels []string) (dir, failure string, err error) {
	rows, err := s.db.Query(ctx, "SELECT dirs FROM rule_runs WHERE id = ?", runID)
	if err != nil || len(rows.Values) == 0 {
		return "", RuleFailUnknown, err
	}
	dirs := map[string]string{}
	if err := json.Unmarshal([]byte(store.Str(rows.Values[0][0])), &dirs); err != nil {
		return "", "", err
	}
	if dir = dirs[dest]; dir != "" {
		rel, err := filepath.Rel(dest, dir)
		if err != nil || !filepath.IsLocal(rel) {
			return "", RuleFailUnknown, nil
		}
		dir, failure = mkdirIn(dest, strings.Split(rel, string(filepath.Separator)))
		return dir, failure, nil
	}
	if !filepath.IsLocal(filepath.Join(levels...)) {
		return "", RuleFailUnknown, nil // FillNaming keeps it inside; this is the backstop
	}
	parent, failure := mkdirIn(dest, levels[:len(levels)-1])
	if failure != "" {
		return "", failure, nil
	}
	for n := 1; n <= maxNumbered; n++ {
		dir = filepath.Join(parent, numberedName(levels[len(levels)-1], n))
		if _, err := os.Lstat(dir); err == nil {
			continue
		}
		dirs[dest] = dir
		b, err := json.Marshal(dirs)
		if err != nil {
			return "", "", err
		}
		if _, err := s.db.Execute(ctx, "UPDATE rule_runs SET dirs = ? WHERE id = ?", string(b), runID); err != nil {
			return "", "", err
		}
		if err := os.Mkdir(dir, 0o755); err == nil {
			return dir, "", nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", RuleFailWrite, nil
		}
	}
	return "", RuleFailWrite, nil
}

// mkdirIn makes each level under dest in turn and returns the last. A level
// there already as a symlink or not a folder is RuleFailUnsafeFolder: a
// rule never writes outside dest through a link in it.
// shortcut: a level swapped for a link between the check and the write is
// not caught; open each level relative to the last (os.Root) if that matters.
func mkdirIn(dest string, levels []string) (dir, failure string) {
	dir = dest
	for _, l := range levels {
		dir = filepath.Join(dir, l)
		if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", RuleFailWrite
		}
		st, err := os.Lstat(dir)
		if err != nil {
			return "", RuleFailWrite
		}
		if !st.IsDir() {
			return "", RuleFailUnsafeFolder
		}
	}
	return dir, ""
}

// maxNumbered bounds the " (n)" candidates tried for one name.
const maxNumbered = 1000

// createNew writes data to a new file named name in dir, numbered past the
// names taken (O_EXCL: nothing is ever replaced). A file there already
// holding data is taken as this one, written before a crash.
func createNew(dir, name string, data []byte) (string, error) {
	for n := 1; n <= maxNumbered; n++ {
		p := filepath.Join(dir, numberedName(name, n))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, data) {
				return p, nil
			}
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(p) // ours, half written
			return "", err
		}
		return p, nil
	}
	return "", fmt.Errorf("%s: no free name", name)
}

// mailDocument is the message as an HTML document to print: its subject,
// sender, recipients and date over its body, sanitized as the reader shows
// it, remote images left out.
func (s *Service) mailDocument(ctx context.Context, msgID int64) (string, error) {
	m, err := s.GetMessage(ctx, msgID, false)
	if err != nil {
		return "", err
	}
	body := m.HTML
	if body == "" {
		body = `<pre style="white-space: pre-wrap; font: inherit">` + html.EscapeString(m.Text) + `</pre>`
	}
	people := strings.Join(append([]string{m.From}, append(m.To, m.Cc...)...), ", ")
	esc := html.EscapeString
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + esc(m.Subject) + `</title><style>` +
		`body{font-family:-apple-system,system-ui,"PingFang SC","Hiragino Sans","Microsoft YaHei","Noto Sans CJK SC",sans-serif;font-size:14px;line-height:1.5;color:#0a0a0a}` +
		`.godwit-head{border-bottom:1px solid #e5e5e5;margin-bottom:16px;padding-bottom:8px}.godwit-head h1{font-size:20px;margin:0 0 4px}.godwit-head p{margin:0;color:#525252}` +
		`</style></head><body><div class="godwit-head"><h1>` + esc(m.Subject) + `</h1><p>` + esc(people) + `</p><p>` +
		esc(m.Date.Local().Format("2006-01-02 15:04")) + `</p></div>` + body + `</body></html>`, nil
}

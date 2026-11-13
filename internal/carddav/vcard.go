package carddav

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"net/mail"
	"net/url"
	"strconv"
	"strings"

	"github.com/emersion/go-message/charset"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

// CardEmail is one normalized email address parsed from a vCard.
type CardEmail struct {
	Pos     int
	AddrKey string
	Address string
	Pref    bool
}

// CardPhoto is either inline image bytes or an external https:// URI from PHOTO.
type CardPhoto struct {
	Data []byte
	URL  string
}

// ParsedCard holds the subset of vCard 3.0 / 4.0 fields Godwit caches (ADR 0040).
type ParsedCard struct {
	FN     string
	Org    string
	Title  string
	Emails []CardEmail
	Photos []CardPhoto
}

type rawEmail struct {
	addrKey string
	address string
	rank    int // 0 = none, 1..100 = lower is more preferred
}

// ParseVCard extracts FN (falling back to ORG when blank), ORG, TITLE, EMAIL
// (with TYPE=PREF and PREF=1..100), and PHOTO from a vCard 3.0 or 4.0 payload.
func ParseVCard(raw string) ParsedCard {
	lines := unfoldVCardLines(raw)
	var out ParsedCard
	var emails []rawEmail

	inCard := false
	subDepth := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		head, val, ok := cutUnquoted(line, ':')
		if !ok {
			continue
		}
		name, params := parsePropHead(head)
		if name == "BEGIN" {
			kind := strings.ToUpper(strings.TrimSpace(val))
			if !inCard {
				if kind == "VCARD" {
					inCard = true
				}
			} else {
				subDepth++
			}
			continue
		}
		if name == "END" {
			kind := strings.ToUpper(strings.TrimSpace(val))
			if inCard {
				if subDepth > 0 {
					subDepth--
				} else if kind == "VCARD" {
					break
				}
			}
			continue
		}
		if !inCard || subDepth > 0 {
			continue
		}

		switch name {
		case "FN":
			if out.FN == "" {
				out.FN = cleanSingleLine(unescapeText(decodeTextValue(val, params)))
			}
		case "ORG":
			if out.Org == "" {
				out.Org = parseOrgValue(decodeTextValue(val, params))
			}
		case "TITLE":
			if out.Title == "" {
				out.Title = cleanSingleLine(unescapeText(decodeTextValue(val, params)))
			}
		case "EMAIL":
			addr, key := normalizeCardEmail(unescapeText(decodeTextValue(val, params)))
			if key == "" {
				continue
			}
			rank := emailPrefRank(params)
			merged := false
			for i := range emails {
				if emails[i].addrKey == key {
					if rank > 0 && (emails[i].rank == 0 || rank < emails[i].rank) {
						emails[i].rank = rank
					}
					merged = true
					break
				}
			}
			if !merged {
				emails = append(emails, rawEmail{addrKey: key, address: addr, rank: rank})
			}
		case "PHOTO":
			if p, ok := parsePhotoValue(val, params); ok {
				out.Photos = append(out.Photos, p)
			}
		}
	}

	if out.FN == "" && out.Org != "" {
		out.FN = out.Org
	}

	minRank := 0
	for _, e := range emails {
		if e.rank > 0 && (minRank == 0 || e.rank < minRank) {
			minRank = e.rank
		}
	}
	if len(emails) > 0 {
		out.Emails = make([]CardEmail, len(emails))
		for i, e := range emails {
			out.Emails[i] = CardEmail{
				Pos:     i,
				AddrKey: e.addrKey,
				Address: e.address,
				Pref:    minRank > 0 && e.rank == minRank,
			}
		}
	}
	return out
}

func unfoldVCardLines(raw string) []string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	rawLines := strings.Split(s, "\n")
	var out []string
	for _, l := range rawLines {
		if len(l) > 0 && (l[0] == ' ' || l[0] == '\t') && len(out) > 0 {
			prev := out[len(out)-1]
			if isQuotedPrintableHead(prev) && strings.HasSuffix(prev, "=") {
				out[len(out)-1] = prev[:len(prev)-1] + l[1:]
			} else {
				out[len(out)-1] = prev + l[1:]
			}
			continue
		}
		if len(out) > 0 {
			prev := out[len(out)-1]
			upper := strings.ToUpper(strings.TrimSpace(l))
			if isQuotedPrintableHead(prev) && strings.HasSuffix(prev, "=") && upper != "END:VCARD" {
				out[len(out)-1] = prev[:len(prev)-1] + l
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

func isQuotedPrintableHead(line string) bool {
	head, _, ok := cutUnquoted(line, ':')
	return ok && strings.Contains(strings.ToUpper(head), "QUOTED-PRINTABLE")
}

func cutUnquoted(s string, sep byte) (before, after string, ok bool) {
	inQuote := false
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func splitUnquoted(s string, sep byte) []string {
	var parts []string
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func parsePropHead(head string) (string, map[string][]string) {
	parts := splitUnquoted(head, ';')
	name := strings.TrimSpace(parts[0])
	if _, after, ok := strings.Cut(name, "."); ok {
		name = after
	}
	name = strings.ToUpper(name)
	params := map[string][]string{}
	for _, rawParam := range parts[1:] {
		rawParam = strings.TrimSpace(rawParam)
		if rawParam == "" {
			continue
		}
		k, v, hasEq := strings.Cut(rawParam, "=")
		if !hasEq {
			tok := strings.ToUpper(strings.Trim(k, `"`))
			switch tok {
			case "B", "BASE64", "QUOTED-PRINTABLE":
				params["ENCODING"] = append(params["ENCODING"], tok)
			default:
				params["TYPE"] = append(params["TYPE"], tok)
			}
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(k))
		for _, item := range splitUnquoted(v, ',') {
			item = strings.TrimSpace(strings.Trim(strings.TrimSpace(item), `"`))
			if item != "" {
				params[key] = append(params[key], item)
			}
		}
	}
	return name, params
}

func firstParam(params map[string][]string, key string) string {
	if vs := params[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func decodeTextValue(val string, params map[string][]string) string {
	enc := strings.ToUpper(firstParam(params, "ENCODING"))
	var raw []byte
	if enc == "QUOTED-PRINTABLE" {
		b, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(val)))
		if err == nil {
			raw = b
		} else {
			raw = []byte(val)
		}
	} else {
		raw = []byte(val)
	}
	cs := strings.TrimSpace(firstParam(params, "CHARSET"))
	if cs != "" && !strings.EqualFold(cs, "utf-8") && !strings.EqualFold(cs, "utf8") && !strings.EqualFold(cs, "us-ascii") {
		if r, err := charset.Reader(cs, bytes.NewReader(raw)); err == nil {
			if decoded, err := io.ReadAll(r); err == nil {
				return string(decoded)
			}
		}
	}
	return string(raw)
}

func unescapeText(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			case ',', ';', '\\', ':':
				b.WriteByte(s[i])
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func cleanSingleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func parseOrgValue(s string) string {
	var segs []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				cur.WriteByte(' ')
			default:
				cur.WriteByte(s[i])
			}
			continue
		}
		if s[i] == ';' {
			if part := cleanSingleLine(cur.String()); part != "" {
				segs = append(segs, part)
			}
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	if part := cleanSingleLine(cur.String()); part != "" {
		segs = append(segs, part)
	}
	return strings.Join(segs, " ")
}

func normalizeCardEmail(s string) (address, addrKey string) {
	s = strings.TrimSpace(s)
	if len(s) >= len("mailto:") && strings.EqualFold(s[:len("mailto:")], "mailto:") {
		s = strings.TrimSpace(s[len("mailto:"):])
	}
	if s == "" {
		return "", ""
	}
	if a, err := mail.ParseAddress(s); err == nil && a.Address != "" {
		addr := strings.TrimSpace(a.Address)
		return addr, strings.ToLower(addr)
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at >= len(s)-1 || strings.LastIndexByte(s, '@') != at || strings.ContainsAny(s, " \t\r\n<>") {
		return "", ""
	}
	return s, strings.ToLower(s)
}

func emailPrefRank(params map[string][]string) int {
	rank := 0
	for _, t := range params["TYPE"] {
		if strings.EqualFold(t, "PREF") {
			rank = 1
		}
	}
	for _, p := range params["PREF"] {
		if n, err := strconv.Atoi(p); err == nil && n >= 1 && n <= 100 {
			if rank == 0 || n < rank {
				rank = n
			}
		}
	}
	return rank
}

func parsePhotoValue(val string, params map[string][]string) (CardPhoto, bool) {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return CardPhoto{}, false
	}
	if len(trimmed) >= len("data:") && strings.EqualFold(trimmed[:len("data:")], "data:") {
		data, err := imagefetch.DataURL(trimmed)
		if err != nil || len(data) == 0 {
			return CardPhoto{}, false
		}
		return CardPhoto{Data: data}, true
	}

	enc := strings.ToUpper(firstParam(params, "ENCODING"))
	valType := strings.ToUpper(firstParam(params, "VALUE"))
	if enc != "B" && enc != "BASE64" {
		cand := unescapeText(trimmed)
		if strings.HasPrefix(strings.ToLower(cand), "http://") {
			return CardPhoto{}, false
		}
		if strings.HasPrefix(strings.ToLower(cand), "https://") {
			u, err := url.Parse(cand)
			if err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil {
				return CardPhoto{URL: u.String()}, true
			}
			return CardPhoto{}, false
		}
		if valType == "URI" || valType == "URL" {
			return CardPhoto{}, false
		}
	}

	if enc == "QUOTED-PRINTABLE" {
		b, err := io.ReadAll(io.LimitReader(quotedprintable.NewReader(strings.NewReader(val)), imagefetch.MaxAvatarBytes+1))
		if err != nil || len(b) == 0 || len(b) > imagefetch.MaxAvatarBytes {
			return CardPhoto{}, false
		}
		return CardPhoto{Data: b}, true
	}

	compact := strings.Join(strings.Fields(trimmed), "")
	if len(compact) == 0 || len(compact) > imagefetch.MaxAvatarBytes*4/3+16 {
		return CardPhoto{}, false
	}
	b, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(compact, "="))
	}
	if err != nil || len(b) == 0 || len(b) > imagefetch.MaxAvatarBytes {
		return CardPhoto{}, false
	}
	return CardPhoto{Data: b}, true
}

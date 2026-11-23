package mail

import (
	"encoding/base64"
	"mime"
	"regexp"
	"strconv"
	"strings"

	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
)

// headerWords decodes RFC 2047 encoded words in any charset go-message knows.
var headerWords = &mime.WordDecoder{CharsetReader: charset.Reader}

var encodedWord = regexp.MustCompile(`=\?([^?\s]+)\?([bBqQ])\?([^?\s]*)\?=`)

// joinEncodedWords merges each run of adjacent encoded words in one charset,
// separated only by white space, into a single word. Mailers split a long
// GBK or Big5 header at byte boundaries, not character ones (#882), and
// mime.WordDecoder converts each word on its own, so a character cut in two
// became U+FFFD. RFC 2047 §6.2 drops the white space between such words, so
// the merged word means the same. A run it cannot decode is left as is.
func joinEncodedWords(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	ms := encodedWord.FindAllStringSubmatchIndex(s, -1)
	if len(ms) < 2 {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(ms); {
		j := i + 1
		for j < len(ms) && strings.TrimLeft(s[ms[j-1][1]:ms[j][0]], " \t\r\n") == "" &&
			strings.EqualFold(wordCharset(s, ms[i]), wordCharset(s, ms[j])) {
			j++
		}
		if j-i > 1 {
			if raw, ok := wordBytes(s, ms[i:j]); ok {
				b.WriteString(s[last:ms[i][0]])
				b.WriteString("=?" + s[ms[i][2]:ms[i][3]] + "?B?" + base64.StdEncoding.EncodeToString(raw) + "?=")
				last = ms[j-1][1]
			}
		}
		i = j
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// wordCharset is a word's charset without an RFC 2231 language suffix.
func wordCharset(s string, m []int) string {
	cs, _, _ := strings.Cut(s[m[2]:m[3]], "*")
	return cs
}

// wordBytes concatenates the words' decoded payloads, still in their charset.
func wordBytes(s string, ms [][]int) ([]byte, bool) {
	var out []byte
	for _, m := range ms {
		text := s[m[6]:m[7]]
		switch s[m[4]] {
		case 'b', 'B':
			d, err := base64.StdEncoding.DecodeString(text)
			if err != nil {
				return nil, false
			}
			out = append(out, d...)
		default:
			for k := 0; k < len(text); k++ {
				switch c := text[k]; {
				case c == '_':
					out = append(out, ' ')
				case c == '=':
					if k+2 >= len(text) {
						return nil, false
					}
					v, err := strconv.ParseUint(text[k+1:k+3], 16, 8)
					if err != nil {
						return nil, false
					}
					out = append(out, byte(v))
					k += 2
				default:
					out = append(out, c)
				}
			}
		}
	}
	return out, true
}

// headerText decodes a text header field such as Subject.
func headerText(h *gomail.Header, key string) string {
	v := joinEncodedWords(h.Get(key))
	if d, err := headerWords.DecodeHeader(v); err == nil {
		return d
	}
	return v
}

// addressList parses an address header field. Besides joining split words, it
// decodes encoded words a mailer put inside a quoted display name
// ("=?GBK?B?...?=" <a@example.cn>): RFC 2047 forbids that, so net/mail keeps
// the name literal, but Foxmail and others send it and other clients decode it.
func addressList(h *gomail.Header, key string) []*gomail.Address {
	v := h.Get(key)
	if v == "" {
		return nil
	}
	l, _ := gomail.ParseAddressList(joinEncodedWords(v)) // partial lists are still useful
	for _, a := range l {
		if strings.Contains(a.Name, "=?") {
			if d, err := headerWords.DecodeHeader(joinEncodedWords(a.Name)); err == nil {
				a.Name = d
			}
		}
	}
	return l
}

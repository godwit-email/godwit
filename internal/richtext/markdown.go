package richtext

import (
	"bytes"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// md is GFM (tables, strikethrough, autolinks) without raw HTML, which
// goldmark omits unless told otherwise.
var md = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify))

// FromMarkdown converts Markdown (a model's answer, say) through the same
// sanitizer as received mail, so it gets no more than mail does. Remote
// images are dropped: loading one would tell its server what was written.
func FromMarkdown(src string) Doc {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return Doc{}
	}
	safe, _ := mail.Sanitize(buf.String(), nil, false)
	d, _ := FromMailHTML(safe, DefaultLimits)
	return d
}

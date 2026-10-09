package mcpserver

import (
	"fmt"
	"strings"
)

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// untrusted writes one mail-derived field as a line of its own:
// <untrusted_email id="<id>" field="<field>">value</untrusted_email>, with
// the value's < > & escaped so it can neither close the marker nor open one.
// id and field are ours (such as "message-12" and "subject"), never mail's.
// Empty values are left out.
func untrusted(b *strings.Builder, id, field, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "<untrusted_email id=%q field=%q>%s</untrusted_email>\n", id, field, escaper.Replace(value))
}

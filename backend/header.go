package backend

import (
	"bytes"
	"slices"
	"strings"
)

// HeaderFields is the fields of raw's header named in names, as they are,
// followed by the blank line that ends a header: Content.Header of a
// backend that downloads whole messages.
func HeaderFields(raw []byte, names []string) []byte {
	var out bytes.Buffer
	keep := false
	for line := range bytes.Lines(raw) {
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			break
		}
		if line[0] != ' ' && line[0] != '\t' {
			name, _, _ := bytes.Cut(line, []byte(":"))
			keep = slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, string(bytes.TrimSpace(name))) })
		}
		if keep {
			out.Write(line)
		}
	}
	out.WriteString("\r\n")
	return out.Bytes()
}

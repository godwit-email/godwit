package testmail_test

import (
	"bytes"
	"github.com/emersion/go-imap/v2"
)

type lit struct{ *bytes.Reader }

func (l lit) Size() int64 { return l.Reader.Size() }

func bytesLiteral(s string) imap.LiteralReader { return lit{bytes.NewReader([]byte(s))} }

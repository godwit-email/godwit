package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"unicode/utf8"

	markitdown "github.com/godwit-email/markitdown-go"
	_ "github.com/godwit-email/markitdown-go/csv"
	_ "github.com/godwit-email/markitdown-go/docx"
	_ "github.com/godwit-email/markitdown-go/html"
	_ "github.com/godwit-email/markitdown-go/pdf"
	_ "github.com/godwit-email/markitdown-go/pptx"
	_ "github.com/godwit-email/markitdown-go/text"
	_ "github.com/godwit-email/markitdown-go/xls"
	_ "github.com/godwit-email/markitdown-go/xlsx"

	"github.com/godwit-email/godwit/core/ai"
)

// attachmentRunes caps the text one read_attachment call shows the model,
// the same as one message body.
const attachmentRunes = 24000

// readAttachment is the read_attachment tool: the attachment's text (PDF,
// Office, CSV, HTML, plain text), fetched on demand and shown like a mail
// body, as untrusted content. No OCR and no archives: images and zips are unsupported.
func (s *Service) readAttachment(ctx context.Context, args json.RawMessage) (any, error) {
	var in idArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	runOf(ctx).scope.MarkReadMail()
	path, info, err := s.GetAttachment(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	res, err := markitdown.Extract(ctx, markitdown.Request{R: f, MIME: info.ContentType, Filename: info.Filename, MaxOut: 4 * attachmentRunes})
	if errors.Is(err, markitdown.ErrUnsupported) {
		return nil, fmt.Errorf("attachment %d (%s) is not a type whose text can be read", in.ID, info.ContentType)
	}
	if err != nil {
		return nil, err
	}
	text, cut := res.Text, res.Partial
	if utf8.RuneCountInString(text) > attachmentRunes {
		text, cut = string([]rune(text)[:attachmentRunes]), true
	}
	head := fmt.Sprintf("Attachment: %s (%s)\n\n", info.Filename, info.ContentType)
	block := ai.Untrusted(fmt.Sprintf("attachment:%d", in.ID), head+text, ai.InjectionQuote(head+text) != "")
	if cut {
		block += truncatedNote
	}
	return map[string]any{"attachment_id": in.ID, "text": block}, nil
}

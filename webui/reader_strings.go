package webui

import "github.com/godwit-email/godwit/internal/i18n"

// readerKeys are the i18n keys the reader page shows, sent in
// ReaderBody.Text: the body's own failure and its retry. The notices
// around the body (remote images, truncation) are native.
var readerKeys = []string{"common.retry", "error.unknown"}

// readerText is readerKeys in the UI language.
func readerText() map[string]string {
	m := make(map[string]string, len(readerKeys))
	for _, k := range readerKeys {
		m[k] = i18n.T(k)
	}
	return m
}

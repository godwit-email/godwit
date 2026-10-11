package webui

import "github.com/godwit-email/godwit/internal/i18n"

// readerKeys are the i18n keys the reader page shows, sent in
// ReaderBody.Text: the body's own failure, its retry and the body frame's
// name. The notices around the body (remote images, truncation) are
// native. The page keeps a zh-CN copy of these for before any body arrives.
var readerKeys = []string{"common.retry", "error.unknown", "reader.body"}

// readerText is readerKeys in the UI language.
func readerText() map[string]string {
	m := make(map[string]string, len(readerKeys))
	for _, k := range readerKeys {
		m[k] = i18n.T(k)
	}
	return m
}

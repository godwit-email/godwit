package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// offline keeps a printed page from loading anything over the network: the
// mail is sanitized already, and this is the backstop (no egress).
var offline = sync.OnceValues(func() (*mygo.ContentRules, error) {
	return mygo.CompileContentRules("godwit-print-offline", `[{"trigger": {"url-filter": "^https?:"}, "action": {"type": "block"}},
		{"trigger": {"url-filter": "^wss?:"}, "action": {"type": "block"}}, {"trigger": {"url-filter": "^ftp:"}, "action": {"type": "block"}}]`)
})

// printPDF lays out html as an A4 PDF in a hidden window's page, for rules
// that export one (core.Options.PrintPDF). The system web view draws it
// with the system's fonts, CJK included, and PrintToPDF puts back the
// ideographs macOS names as radicals, so its text copies and searches.
func printPDF(ctx context.Context, html string) ([]byte, error) {
	rules, err := offline()
	if err != nil {
		return nil, err
	}
	win := mygo.NewWindow(mygo.WindowOptions{Hidden: true, Width: 800, Height: 1100})
	defer win.Destroy()
	page := win.Page()
	page.SetContentRules(rules)
	loaded := make(chan struct{}, 1)
	off := page.OnDidFinishLoad(func() {
		select {
		case loaded <- struct{}{}:
		default:
		}
	})
	defer off()
	page.LoadHTML(html, "")
	select {
	case <-loaded:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, errors.New("printing a PDF: the page did not load")
	}
	return page.PrintToPDF(mygo.PDFOptions{PageSize: mygo.PageA4, Background: true})
}

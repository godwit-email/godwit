package mail

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// trackerPaths are substrings of the URLs of known open-tracking endpoints.
// The heuristic is small on purpose: whatever else a mail loads is served
// through the image proxy, which hides who and when anyway.
var trackerPaths = []string{
	"/edm/catcher",               // TikTok
	"list-manage.com/track/open", // Mailchimp
	"/wf/open",                   // SendGrid
	"/track/open",
	"google-analytics.com/collect",
}

var styleSize = regexp.MustCompile(`(?i)^\s*(width|height)\s*:\s*(\d+)(?:px)?\s*$`)

// isTracker reports an <img> that is a tracking pixel: declared 1×1 or
// smaller (by attributes or style) or invisible, or a known tracker's URL.
func isTracker(n *html.Node) bool {
	src, _ := attr(n, "src")
	l := strings.ToLower(src)
	for _, p := range trackerPaths {
		if strings.Contains(l, p) {
			return true
		}
	}
	size := map[string]int{}
	set := func(k, v string) {
		v = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(v)), "px")
		if i, err := strconv.Atoi(v); err == nil {
			size[k] = i
		}
	}
	for _, k := range []string{"width", "height"} {
		if v, ok := attr(n, k); ok {
			set(k, v)
		}
	}
	if st, ok := attr(n, "style"); ok {
		for _, decl := range strings.Split(st, ";") {
			if m := styleSize.FindStringSubmatch(decl); m != nil {
				set(strings.ToLower(m[1]), m[2])
			}
		}
	}
	w, hasW := size["width"]
	h, hasH := size["height"]
	return (hasW && hasH && w <= 1 && h <= 1) || (hasW && w == 0) || (hasH && h == 0)
}

package sysproxy

import (
	"net/url"
	"testing"
)

func TestBypassed(t *testing.T) {
	list := []string{"*.local", "169.254/16", "corp.example", ".internal.example", "<local>", "10.0.0.0/8", ""}
	for host, want := range map[string]bool{
		"printer.local":         true,
		"169.254.10.1":          true,
		"corp.example":          true,
		"mail.corp.example":     true,
		"a.internal.example":    true,
		"intranet":              true, // <local>: no dot
		"10.1.2.3":              true,
		"images.example":        false,
		"notcorp.example":       false,
		"8.8.8.8":               false,
		"internal.example":      false, // .suffix needs a label before it
		"CORP.EXAMPLE.":         true,
		"169.254.10.1.nip.io.x": false,
	} {
		if got := bypassed(host, list); got != want {
			t.Errorf("bypassed(%q) = %v", host, got)
		}
	}
}

func TestParseWindows(t *testing.T) {
	s := parseWindows("127.0.0.1:7890", "localhost;*.lan;<local>")
	if s.http.String() != "http://127.0.0.1:7890" || s.https != s.http || s.socks != nil || len(s.bypass) != 3 {
		t.Errorf("one for all: %+v", s)
	}
	s = parseWindows("http=proxy:80;https=proxy:443;socks=127.0.0.1:1080", "")
	if s.http.String() != "http://proxy:80" || s.https.String() != "http://proxy:443" || s.socks.String() != "socks5://127.0.0.1:1080" || s.bypass != nil {
		t.Errorf("per scheme: %+v", s)
	}
	if s := parseWindows("garbage", ""); s.http != nil {
		t.Errorf("no port: %+v", s)
	}
}

func TestForEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "corp.example")
	u, _ := url.Parse("https://images.example/a.png")
	if p := For(u); p == nil || p.Host != "127.0.0.1:7890" {
		t.Errorf("HTTPS_PROXY: %v", p)
	}
	u, _ = url.Parse("https://cdn.corp.example/a.png")
	if p := For(u); p != nil {
		t.Errorf("NO_PROXY: %v", p)
	}
}

package avatar

import (
	"reflect"
	"testing"
)

func TestPersonalDomain(t *testing.T) {
	for _, tt := range []struct {
		domain string
		want   bool
	}{
		{"GMAIL.COM", false}, {" gmail.com. ", false}, {"proton.me", false}, {"pm.me", false}, {"fastmail.com", false},
		{"hey.com", false}, {"gmx.de", false}, {"tuta.io", false}, {"mail.ru", false}, {"企业.中国", true},
		{"me.example.com", true}, {"gmail.com.example.org", true}, {"mail.gmail.com", true},
		{"", false}, {"co.uk", false}, {"localhost", false}, {"127.0.0.1", false}, {"[::1]", false},
		{"https://example.com", false}, {"x@example.com", false}, {"example.com:443", false}, {"bad_label.com", false},
	} {
		if got := PersonalDomain(tt.domain, []string{"gmail.com", "QQ.COM"}); got != tt.want {
			t.Errorf("PersonalDomain(%q) = %v, want %v", tt.domain, got, tt.want)
		}
	}
	if PersonalDomain("QQ.com", []string{"QQ.COM"}) {
		t.Error("provider domain not excluded")
	}
	if PersonalDomain("xn--bcher-kva.de", []string{"BÜCHER.DE"}) {
		t.Error("IDN provider not excluded")
	}
}

func TestFaviconSites(t *testing.T) {
	for _, tt := range []struct {
		domain string
		want   []string
	}{
		{"Mail.Example.CO.UK.", []string{"mail.example.co.uk", "example.co.uk"}},
		{"example.co.uk", []string{"example.co.uk"}}, {"BÜCHER.de", []string{"xn--bcher-kva.de"}},
		{"mail.BÜCHER.de", []string{"mail.xn--bcher-kva.de", "xn--bcher-kva.de"}},
		{"co.uk", nil}, {"com", nil}, {"127.0.0.1", nil}, {"example.com/path", nil},
	} {
		if got := FaviconSites(tt.domain); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("FaviconSites(%q) = %v, want %v", tt.domain, got, tt.want)
		}
	}
}

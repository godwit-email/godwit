package auth_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/conn"
)

type fakeResolver struct {
	mx  map[string][]*net.MX
	srv map[string][]*net.SRV // key: service+"/"+name
}

func (f fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if name == "dnsfail.example" {
		return nil, context.DeadlineExceeded
	}
	if v, ok := f.mx[name]; ok {
		return v, nil
	}
	return nil, errors.New("no such host")
}

func (f fakeResolver) LookupSRV(_ context.Context, service, _, name string) (string, []*net.SRV, error) {
	if v, ok := f.srv[service+"/"+name]; ok {
		return "", v, nil
	}
	return "", nil, errors.New("no such host")
}

func TestDetect(t *testing.T) {
	r := fakeResolver{
		mx: map[string][]*net.MX{
			"corp.example":      {{Host: "alt1.aspmx.l.google.com.", Pref: 20}, {Host: "aspmx.l.google.com.", Pref: 1}},
			"biz.example":       {{Host: "mxbiz1.qq.com.", Pref: 5}},
			"fs.example":        {{Host: "mx2.feishu.cn.", Pref: 20}, {Host: "mx1.feishu.cn.", Pref: 10}},
			"lark.example":      {{Host: "mx-sg.larksuite.com.", Pref: 10}},
			"ding.example":      {{Host: "mx1.em.dingtalk.com.", Pref: 10}},
			"coremail.example":  {{Host: "mx2.c3.icoremail.net.", Pref: 10}, {Host: "mx.c3.icoremail.net.", Pref: 5}},
			"coremail1.example": {{Host: "mx.icoremail.net.", Pref: 5}},
			"coreapex.example":  {{Host: "icoremail.net.", Pref: 5}},
			"coredeep.example":  {{Host: "a.mx.c3.icoremail.net.", Pref: 5}},
			"alimail.example":   {{Host: "mxn.mxhichina.com.", Pref: 5}},
			"aliqiye.example":   {{Host: "mx1.qiye.aliyun.com.", Pref: 5}},
			"ntesqiye.example":  {{Host: "qiye163mx01.mxmail.netease.com.", Pref: 5}},
			"nteshz.example":    {{Host: "hzmx01.mxmail.netease.com.", Pref: 5}},
			"ntesym.example":    {{Host: "mx.ym.163.com.", Pref: 5}},
			"mail263.example":   {{Host: "mx.263xmail.com.", Pref: 5}},
			"sina.example":      {{Host: "mx.sina.net.", Pref: 5}},
			"zoho.example":      {{Host: "mx.zoho.com.cn.", Pref: 5}},
			"fastmail.example":  {{Host: "in1-smtp.messagingengine.com.", Pref: 10}},
			"srv.example":       {{Host: "mx.unknown.example.", Pref: 5}},
		},
		srv: map[string][]*net.SRV{
			"imaps/srv.example":       {{Target: "mail.srv.example.", Port: 1993}},
			"submissions/srv.example": {{Target: "smtp.srv.example.", Port: 1465}},
		},
	}
	cases := []struct {
		email, id string
		src       auth.Source
		imapHost  string
	}{
		{"a@Gmail.com", "gmail", "domain", "imap.gmail.com"},
		{"a@yeah.net", "netease126", "domain", "imap.yeah.net"},
		{"a@ymail.com", "yahoo", "domain", "imap.mail.yahoo.com"},
		{"a@aol.com", "aol", "domain", "imap.aol.com"},
		{"a@live.cn", "outlook", "domain", "outlook.office365.com"},
		{"a@corp.example", "gmail", "mx", "imap.gmail.com"},
		{"a@biz.example", "exmail", "mx", "imap.exmail.qq.com"},
		{"a@fs.example", "feishu", "mx", "imap.feishu.cn"},
		{"a@lark.example", "lark", "mx", "imap.larksuite.com"},
		{"a@ding.example", "dingtalk", "mx", "imap.em.dingtalk.com"},
		{"a@coremail.example", "other", "mx", "c3.icoremail.net"},
		{"a@coremail1.example", "other", "mx", "imap.icoremail.net"},
		{"a@coreapex.example", "other", "mx", "imap.icoremail.net"},
		{"a@coredeep.example", "other", "mx", "imap.icoremail.net"},
		{"a@alimail.example", "other", "mx", "imap.qiye.aliyun.com"},
		{"a@aliqiye.example", "other", "mx", "imap.qiye.aliyun.com"},
		{"a@ntesqiye.example", "other", "mx", "imap.qiye.163.com"},
		{"a@nteshz.example", "other", "mx", "imap.qiye.163.com"},
		{"a@ntesym.example", "other", "mx", "imap.ym.163.com"},
		{"a@mail263.example", "other", "mx", "imap.263.net"},
		{"a@sina.example", "other", "mx", "imap.sina.net"},
		{"a@zoho.example", "other", "mx", "imappro.zoho.com.cn"},
		{"a@fastmail.example", "other", "mx", "imap.fastmail.com"},
		{"a@srv.example", "other", "srv", "mail.srv.example"},
		{"a@none.example", "other", "guess", "imap.none.example"},
		{"a@dnsfail.example", "other", "guess", "imap.dnsfail.example"},
	}
	for _, c := range cases {
		p, src, err := auth.Detect(context.Background(), r, c.email)
		if err != nil {
			t.Fatalf("%s: %v", c.email, err)
		}
		if p.ID != c.id || src != c.src || p.IMAP.Host != c.imapHost {
			t.Errorf("%s: got %s/%s/%s want %s/%s/%s", c.email, p.ID, src, p.IMAP.Host, c.id, c.src, c.imapHost)
		}
		if c.src == "srv" && (p.IMAP.Port != 1993 || p.SMTP.Host != "smtp.srv.example" || p.SMTP.Port != 1465) {
			t.Errorf("srv: got imap %d smtp %s:%d", p.IMAP.Port, p.SMTP.Host, p.SMTP.Port)
		}
		if c.id == "other" && p.Login != "password" {
			t.Errorf("%s: login %q", c.email, p.Login)
		}
	}
}

func TestDetectMailserversDatabase(t *testing.T) {
	r := fakeResolver{mx: map[string][]*net.MX{
		"web.de":          {{Host: "mx-ha02.web.de.", Pref: 1}},
		"custom.example":  {{Host: "mx-ha02.web.de.", Pref: 1}},
		"gmail-hosted.de": {{Host: "aspmx.l.google.com.", Pref: 1}},
		"mit.edu":         {{Host: "mit-edu.mail.protection.outlook.com.", Pref: 1}},
	}}
	cases := []struct {
		email, id string
		src       auth.Source
		imapHost  string
		username  string
	}{
		{"a@web.de", "other", "domain", "imap.web.de", "a"},
		{"a@custom.example", "other", "mx", "imap.web.de", ""}, // web.de's template is for its own addresses
		{"a@gmail-hosted.de", "gmail", "mx", "imap.gmail.com", ""},
		{"a@goo.jp", "other", "domain", "imap.mail.goo.jp", ""}, // no MX answer
		{"a@mit.edu", "other", "guess", "imap.mit.edu", ""},     // OAuth only
	}
	for _, c := range cases {
		p, src, err := auth.Detect(context.Background(), r, c.email)
		if err != nil {
			t.Fatalf("%s: %v", c.email, err)
		}
		if p.ID != c.id || src != c.src || p.IMAP.Host != c.imapHost || p.Username != c.username {
			t.Errorf("%s: got %s/%s/%s/%q want %s/%s/%s/%q", c.email, p.ID, src, p.IMAP.Host, p.Username, c.id, c.src, c.imapHost, c.username)
		}
	}
}

func TestDetectRejectsBadAddress(t *testing.T) {
	for _, e := range []string{"not-an-email", "a@", "@x.com"} {
		if _, _, err := auth.Detect(context.Background(), fakeResolver{}, e); err == nil {
			t.Errorf("%q: want error", e)
		}
	}
}

func TestProvidersComplete(t *testing.T) {
	want := []string{"gmail", "outlook", "netease163", "netease126", "qq", "exmail", "feishu", "lark", "dingtalk", "icloud", "yahoo", "aol"}
	ps := auth.ListProviders()
	if len(ps) != len(want) {
		t.Fatalf("got %d providers", len(ps))
	}
	for i, p := range ps {
		if p.ID != want[i] {
			t.Errorf("order[%d] = %s, want %s", i, p.ID, want[i])
		}
		for _, s := range []auth.Server{p.IMAP, p.SMTP} {
			if s.Host == "" || s.Port <= 0 || s.Security == conn.None || s.Security == "" {
				t.Errorf("%s: bad server %+v", p.ID, s)
			}
		}
		if p.Login != "oauth" && p.HelpURL == "" {
			t.Errorf("%s: empty HelpURL", p.ID)
		}
		if p.ID == "outlook" && p.OAuth == nil {
			t.Error("outlook: nil OAuth")
		}
		if got, ok := auth.ByID(p.ID); !ok || got.ID != p.ID {
			t.Errorf("ByID(%s)", p.ID)
		}
	}
}

func TestDetectMXBoundaryPriorityAndSRVFallback(t *testing.T) {
	r := fakeResolver{
		mx: map[string][]*net.MX{
			"prio.example":  {{Host: "mxbiz1.qq.com.", Pref: 20}, {Host: "mx1.feishu.cn.", Pref: 1}},
			"evil1.example": {{Host: "mx.evilgoogle.com.", Pref: 1}},
			"evil2.example": {{Host: "mxbiz1.evilqq.com.", Pref: 1}},
			"evil3.example": {{Host: "mx1.notfeishu.cn.", Pref: 1}},
			"evil4.example": {{Host: "x.googlemail.com.evil.example.", Pref: 1}},
		},
		srv: map[string][]*net.SRV{
			"imaps/sub.example":          {{Target: "imap.sub.example.", Port: 993}},
			"submission/sub.example":     {{Target: "smtp.sub.example.", Port: 587}},
			"imaps/dot.example":          {{Target: "imap.dot.example.", Port: 993}},
			"submissions/dot.example":    {{Target: ".", Port: 0}},
			"submission/dot.example":     {{Target: "smtp.dot.example.", Port: 587}},
			"imaps/alldot.example":       {{Target: "imap.alldot.example.", Port: 993}},
			"submissions/alldot.example": {{Target: ".", Port: 0}},
		},
	}
	get := func(email string) (auth.Provider, auth.Source) {
		p, s, err := auth.Detect(context.Background(), r, email)
		if err != nil {
			t.Fatal(err)
		}
		return p, s
	}
	if p, s := get("a@prio.example"); p.ID != "feishu" || s != "mx" {
		t.Errorf("lowest preference must win: got %s/%s", p.ID, s)
	}
	for _, d := range []string{"evil1", "evil2", "evil3", "evil4"} {
		if p, s := get("a@" + d + ".example"); p.ID != "other" || s != "guess" {
			t.Errorf("%s: lookalike MX matched %s/%s", d, p.ID, s)
		}
	}
	p, s := get("a@sub.example")
	if s != "srv" || p.IMAP.Security != conn.TLS || p.SMTP.Security != conn.StartTLS || p.SMTP.Port != 587 || p.SMTP.Host != "smtp.sub.example" {
		t.Errorf("submission fallback: %s %+v %+v", s, p.IMAP, p.SMTP)
	}
	p, s = get("a@dot.example")
	if s != "srv" || p.SMTP.Host != "smtp.dot.example" || p.SMTP.Security != conn.StartTLS {
		t.Errorf("dot target: %s %+v", s, p.SMTP)
	}
	if _, s = get("a@alldot.example"); s != "guess" {
		t.Errorf("all-dot SMTP: source %s, want guess", s)
	}
}

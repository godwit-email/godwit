// Package auth holds the provider table and account auto-detection.
package auth

import "github.com/godwit-email/godwit/core/conn"

type Server struct {
	Host     string
	Port     int
	Security conn.Security
}

// OAuthConfig describes a provider's OAuth 2.0 endpoints (flow lives elsewhere).
type OAuthConfig struct {
	ClientID, AuthURL, TokenURL string
	Scopes                      []string
}

type Provider struct {
	ID, Name   string
	Domains    []string
	IMAP, SMTP Server
	Login      string // app_password | authorization_code | client_password | password | oauth
	HelpURL    string
	OAuth      *OAuthConfig
}

// MicrosoftClientID is read when the outlook provider is built.
var MicrosoftClientID = ""

const neteaseHelp = "https://help.mail.163.com/" // the 163 FAQ article URL answers 400; homepage instead

func tls(h string) Server   { return Server{h, 993, conn.TLS} }
func smtps(h string) Server { return Server{h, 465, conn.TLS} }

func table() []Provider {
	return []Provider{
		{ID: "gmail", Name: "Gmail", Domains: []string{"gmail.com", "googlemail.com"},
			IMAP: tls("imap.gmail.com"), SMTP: smtps("smtp.gmail.com"), Login: "app_password",
			HelpURL: "https://support.google.com/accounts/answer/185833"},
		{ID: "outlook", Name: "Outlook", Domains: []string{"outlook.com", "hotmail.com", "live.com", "msn.com", "hotmail.co.uk", "outlook.jp"},
			IMAP: tls("outlook.office365.com"), SMTP: Server{"smtp-mail.outlook.com", 587, conn.StartTLS}, Login: "oauth",
			OAuth: &OAuthConfig{
				ClientID: MicrosoftClientID,
				AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
				TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
				Scopes: []string{"openid", "email", "offline_access",
					"https://outlook.office.com/IMAP.AccessAsUser.All", "https://outlook.office.com/SMTP.Send"},
			}},
		{ID: "netease163", Name: "163 Mail", Domains: []string{"163.com"},
			IMAP: tls("imap.163.com"), SMTP: smtps("smtp.163.com"), Login: "authorization_code", HelpURL: neteaseHelp},
		{ID: "netease126", Name: "126 Mail", Domains: []string{"126.com", "yeah.net"},
			IMAP: tls("imap.126.com"), SMTP: smtps("smtp.126.com"), Login: "authorization_code", HelpURL: neteaseHelp},
		{ID: "qq", Name: "QQ Mail", Domains: []string{"qq.com", "foxmail.com", "vip.qq.com"},
			IMAP: tls("imap.qq.com"), SMTP: smtps("smtp.qq.com"), Login: "authorization_code",
			HelpURL: "https://service.mail.qq.com/detail/0/75"},
		{ID: "exmail", Name: "Tencent Exmail",
			IMAP: tls("imap.exmail.qq.com"), SMTP: smtps("smtp.exmail.qq.com"), Login: "client_password",
			HelpURL: "https://open.work.weixin.qq.com/help2/pc/19886"},
		// Feishu and DingTalk Mail are on company domains, found by MX.
		// Feishu's servers and its 专用密码 are from its help article; an admin
		// enables third-party clients first.
		{ID: "feishu", Name: "Feishu Mail",
			IMAP: tls("imap.feishu.cn"), SMTP: smtps("smtp.feishu.cn"), Login: "app_password",
			HelpURL: "https://www.feishu.cn/hc/zh-CN/articles/902478147400"},
		// DingTalk Mail runs on Alibaba Mail; its client hosts are the em.dingtalk.com
		// names its certificate carries (no official page lists them). A client
		// takes the password, or the 三方客户端安全密码 when one is set; the help
		// is Alibaba Mail's.
		{ID: "dingtalk", Name: "DingTalk Mail",
			IMAP: tls("imap.em.dingtalk.com"), SMTP: smtps("smtp.em.dingtalk.com"), Login: "client_password",
			HelpURL: "https://help.aliyun.com/zh/document_detail/36576.html"}, // Alibaba Mail's client settings and 安全密码
		{ID: "icloud", Name: "iCloud Mail", Domains: []string{"icloud.com", "me.com", "mac.com"},
			IMAP: tls("imap.mail.me.com"), SMTP: Server{"smtp.mail.me.com", 587, conn.StartTLS}, Login: "app_password",
			HelpURL: "https://support.apple.com/102654"},
		{ID: "fastmail", Name: "Fastmail", Domains: []string{"fastmail.com", "fastmail.fm"},
			IMAP: tls("imap.fastmail.com"), SMTP: smtps("smtp.fastmail.com"), Login: "app_password",
			HelpURL: "https://www.fastmail.help/hc/en-us/articles/360058752854-App-passwords"},
	}
}

// ListProviders returns the known providers in display order (no "other").
func ListProviders() []Provider { return table() }

func ByID(id string) (Provider, bool) {
	for _, p := range table() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

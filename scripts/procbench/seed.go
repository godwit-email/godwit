package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/godwit-email/godwit/core"
)

const benchPassword = "bench-password-718"

type seedScale struct {
	Accounts       int
	ExtraFolders   int
	InboxMessages  int
	FolderMessages int
}

type loopbackMail struct {
	IMAPHost string
	IMAPPort int
	SMTPHost string
	SMTPPort int

	imapLn  net.Listener
	imapSrv *imapserver.Server
	smtpLn  net.Listener
	smtpSrv *smtp.Server
}

func (m *loopbackMail) Close() {
	if m.imapLn != nil {
		_ = m.imapLn.Close()
	}
	if m.imapSrv != nil {
		_ = m.imapSrv.Close()
	}
	if m.smtpLn != nil {
		_ = m.smtpLn.Close()
	}
	if m.smtpSrv != nil {
		_ = m.smtpSrv.Close()
	}
}

type silentIMAPLog struct{}

func (silentIMAPLog) Printf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !strings.Contains(msg, net.ErrClosed.Error()) &&
		!strings.Contains(msg, "EOF") &&
		!strings.Contains(msg, "broken pipe") &&
		!strings.Contains(msg, "connection reset by peer") {
		log.Print(msg)
	}
}

type benchSMTPBackend struct{}

func (benchSMTPBackend) NewSession(*smtp.Conn) (smtp.Session, error) { return &benchSMTPSession{}, nil }

type benchSMTPSession struct{}

func (*benchSMTPSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (*benchSMTPSession) Auth(mech string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, _, pass string) error {
		if pass != benchPassword {
			return errors.New("invalid password")
		}
		return nil
	}), nil
}
func (*benchSMTPSession) Mail(string, *smtp.MailOptions) error { return nil }
func (*benchSMTPSession) Rcpt(string, *smtp.RcptOptions) error { return nil }
func (*benchSMTPSession) Data(r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}
func (*benchSMTPSession) Reset()        {}
func (*benchSMTPSession) Logout() error { return nil }

func accountEmail(idx int) string { return fmt.Sprintf("user%d@bench.local", idx+1) }

func startLoopbackMail(scale seedScale) (*loopbackMail, error) {
	mem := imapmemserver.New()
	baseTime := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	bodyPad := strings.Repeat("Deterministic benchmark message line for process baseline measurement.\r\n", 12)

	for a := range scale.Accounts {
		email := accountEmail(a)
		u := imapmemserver.NewUser(email, benchPassword)
		for _, std := range []string{"INBOX", "Sent", "Trash", "Archive"} {
			if err := u.Create(std, &imap.CreateOptions{}); err != nil {
				return nil, err
			}
		}
		appendFolder := func(folder, tag string, count int) error {
			for m := range count {
				threadIdx := m / 4
				step := m % 4
				msgID := fmt.Sprintf("<%s-a%d-t%d-s%d@bench.local>", tag, a+1, threadIdx, step)
				rootID := fmt.Sprintf("<%s-a%d-t%d-s0@bench.local>", tag, a+1, threadIdx)
				subj := fmt.Sprintf("[%s #%d] Quarterly planning thread %04d", tag, a+1, threadIdx+1)
				var extraHdr string
				if step > 0 {
					subj = "Re: " + subj
					extraHdr = fmt.Sprintf("In-Reply-To: %s\r\nReferences: %s\r\n", rootID, rootID)
				}
				ts := baseTime.Add(time.Duration(a*100000+m*60) * time.Second)
				raw := fmt.Sprintf(
					"From: Sender %d <sender%d@example.org>\r\n"+
						"To: %s\r\n"+
						"Subject: %s\r\n"+
						"Date: %s\r\n"+
						"Message-ID: %s\r\n"+
						"%s"+
						"MIME-Version: 1.0\r\n"+
						"Content-Type: text/plain; charset=utf-8\r\n\r\n"+
						"Account %d folder %s message %d.\r\n%s",
					(m%25)+1, (m%25)+1, email, subj, ts.Format(time.RFC1123Z), msgID, extraHdr, a+1, folder, m+1, bodyPad,
				)
				var flags []imap.Flag
				if m%2 == 0 {
					flags = []imap.Flag{imap.FlagSeen}
				}
				if _, err := u.Append(folder, bytes.NewReader([]byte(raw)), &imap.AppendOptions{
					Time:  ts,
					Flags: flags,
				}); err != nil {
					return err
				}
			}
			return nil
		}
		if err := appendFolder("INBOX", "inbox", scale.InboxMessages); err != nil {
			return nil, err
		}
		for f := range scale.ExtraFolders {
			name := fmt.Sprintf("Projects/%02d", f+1)
			if err := u.Create(name, &imap.CreateOptions{}); err != nil {
				return nil, err
			}
			if err := appendFolder(name, fmt.Sprintf("p%02d", f+1), scale.FolderMessages); err != nil {
				return nil, err
			}
		}
		mem.AddUser(u)
	}

	imapSrv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			imap.CapIdle:      {},
			imap.CapMove:      {},
			imap.CapUIDPlus:   {},
		},
		InsecureAuth: true,
		Logger:       silentIMAPLog{},
	})
	imapLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() { _ = imapSrv.Serve(imapLn) }()

	smtpSrv := smtp.NewServer(benchSMTPBackend{})
	smtpSrv.AllowInsecureAuth = true
	smtpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = imapLn.Close()
		_ = imapSrv.Close()
		return nil, err
	}
	go func() { _ = smtpSrv.Serve(smtpLn) }()

	ih, ip, err := splitHostPort(imapLn.Addr().String())
	if err != nil {
		_ = imapLn.Close()
		_ = smtpLn.Close()
		return nil, err
	}
	sh, sp, err := splitHostPort(smtpLn.Addr().String())
	if err != nil {
		_ = imapLn.Close()
		_ = smtpLn.Close()
		return nil, err
	}
	return &loopbackMail{
		IMAPHost: ih,
		IMAPPort: ip,
		SMTPHost: sh,
		SMTPPort: sp,
		imapLn:   imapLn,
		imapSrv:  imapSrv,
		smtpLn:   smtpLn,
		smtpSrv:  smtpSrv,
	}, nil
}

func splitHostPort(addr string) (string, int, error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(p)
	return h, port, err
}

func seedDataDir(ctx context.Context, dataDir string, srv *loopbackMail, scale seedScale) error {
	svc, err := core.New(ctx, core.Options{DataDir: dataDir, AutoSync: false})
	if err != nil {
		return err
	}
	defer svc.Close()

	st, err := svc.GetSettings(ctx)
	if err != nil {
		return err
	}
	st.KeepInBackground = true
	st.ShowTrayIcon = true
	st.AutoLabel = false
	if _, err := svc.SetSettings(ctx, st); err != nil {
		return err
	}

	for a := range scale.Accounts {
		email := accountEmail(a)
		acc, err := svc.AddAccount(ctx, core.AccountInput{
			Email:     email,
			Name:      fmt.Sprintf("Bench User %d", a+1),
			Username:  email,
			Password:  benchPassword,
			IsDefault: a == 0,
			Sync:      &core.SyncRange{}, // sync full mailbox deterministically
			Receiving: core.Server{
				Protocol: "imap",
				Host:     srv.IMAPHost,
				Port:     srv.IMAPPort,
				Security: core.SecurityNone,
			},
			Sending: core.Server{
				Protocol: "smtp",
				Host:     srv.SMTPHost,
				Port:     srv.SMTPPort,
				Security: core.SecurityNone,
			},
		})
		if err != nil {
			return fmt.Errorf("add account %s: %w", email, err)
		}
		if err := svc.SyncNow(ctx, acc.ID); err != nil {
			return fmt.Errorf("initial sync %s: %w", email, err)
		}
	}
	return nil
}

func cleanupSeededAccounts(ctx context.Context, dataDir string) error {
	svc, err := core.New(ctx, core.Options{DataDir: dataDir, AutoSync: false})
	if err != nil {
		return err
	}
	defer svc.Close()
	accs, err := svc.ListAccounts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, acc := range accs {
		if err := svc.RemoveAccount(ctx, acc.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

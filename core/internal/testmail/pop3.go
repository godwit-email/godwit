package testmail

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// POP3 is a running in-memory POP3 server with one maildrop, alice/secret.
// It speaks USER/PASS, STLS, STAT, LIST, UIDL, RETR, TOP, DELE, RSET, NOOP
// and QUIT; deletions take effect at QUIT, as RFC 1939 says.
type POP3 struct {
	Addr  string
	Roots *x509.CertPool // trusts the certificate STLS presents

	tls *tls.Config
	ln  net.Listener

	mu         sync.Mutex
	msgs       []pop3Msg
	next       int
	rejectAuth bool
	rejectDele bool
	noUIDL     bool
	noTOP      bool
	retrLeft   int // RETRs that succeed before the rest fail; -1 for no limit
	stls       int
	dele       int
}

type pop3Msg struct {
	uid string
	raw []byte
}

// StartPOP3 starts a server on 127.0.0.1:0, closed when t ends.
func StartPOP3(t testing.TB) *POP3 {
	t.Helper()
	cert := httptest.NewTLSServer(nil) // only for its 127.0.0.1 certificate
	s := &POP3{tls: cert.TLS, Roots: x509.NewCertPool(), retrLeft: -1}
	s.Roots.AddCert(cert.Certificate())
	cert.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln, s.Addr = ln, ln.Addr().String()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

// Port is the port the server listens on.
func (s *POP3) Port() int {
	_, p, _ := net.SplitHostPort(s.Addr)
	n, _ := strconv.Atoi(p)
	return n
}

// Deliver appends raw to the maildrop and returns its UIDL.
func (s *POP3) Deliver(raw []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	uid := fmt.Sprintf("uid-%d", s.next)
	s.msgs = append(s.msgs, pop3Msg{uid, raw})
	return uid
}

// Remove deletes the message uid from the maildrop, as another client would.
func (s *POP3) Remove(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = slices.DeleteFunc(s.msgs, func(m pop3Msg) bool { return m.uid == uid })
}

// UIDs is the maildrop's UIDLs, oldest first.
func (s *POP3) UIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		out = append(out, m.uid)
	}
	return out
}

// RejectAuth makes the server refuse alice's password from now on.
func (s *POP3) RejectAuth() { s.mu.Lock(); s.rejectAuth = true; s.mu.Unlock() }

// RejectDele makes the server refuse DELE from now on.
func (s *POP3) RejectDele() { s.mu.Lock(); s.rejectDele = true; s.mu.Unlock() }

// FailRETR makes every RETR after the next n fail; n < 0 lets them all succeed again.
func (s *POP3) FailRETR(n int) { s.mu.Lock(); s.retrLeft = n; s.mu.Unlock() }

// DisableUIDL makes the server answer UIDL with -ERR, as one without it does.
func (s *POP3) DisableUIDL() { s.mu.Lock(); s.noUIDL = true; s.mu.Unlock() }

// DisableTOP makes the server refuse TOP, which RFC 1939 leaves optional.
func (s *POP3) DisableTOP() { s.mu.Lock(); s.noTOP = true; s.mu.Unlock() }

// STLS is how many connections the server has upgraded with STLS.
func (s *POP3) STLS() int { s.mu.Lock(); defer s.mu.Unlock(); return s.stls }

// Deletes is how many DELE commands took effect at a QUIT.
func (s *POP3) Deletes() int { s.mu.Lock(); defer s.mu.Unlock(); return s.dele }

func (s *POP3) serve(nc net.Conn) {
	defer func() { nc.Close() }()
	r, w := bufio.NewReader(nc), bufio.NewWriter(nc)
	say := func(lines ...string) {
		for _, l := range lines {
			w.WriteString(l + "\r\n")
		}
		w.Flush()
	}
	say("+OK POP3 ready")
	var user string
	var drop []pop3Msg            // the maildrop as it was at sign-in
	var deleted []bool            // marked by DELE
	msg := func(arg string) int { // the 0-based index arg names, -1 if none
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > len(drop) || deleted[n-1] {
			return -1
		}
		return n - 1
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) == 0 {
			say("-ERR empty command")
			continue
		}
		cmd, args := strings.ToUpper(f[0]), f[1:]
		arg := func(i int) string {
			if i < len(args) {
				return args[i]
			}
			return ""
		}
		authed := drop != nil
		switch {
		case cmd == "QUIT":
			if authed {
				s.mu.Lock()
				for i, d := range deleted {
					if d {
						uid := drop[i].uid
						s.msgs = slices.DeleteFunc(s.msgs, func(m pop3Msg) bool { return m.uid == uid })
						s.dele++
					}
				}
				s.mu.Unlock()
			}
			say("+OK bye")
			return
		case cmd == "STLS" && !authed:
			say("+OK begin TLS")
			tc := tls.Server(nc, s.tls)
			if tc.Handshake() != nil {
				return
			}
			s.mu.Lock()
			s.stls++
			s.mu.Unlock()
			nc = tc
			r, w = bufio.NewReader(nc), bufio.NewWriter(nc)
		case cmd == "USER" && !authed:
			user = arg(0)
			say("+OK")
		case cmd == "PASS" && !authed:
			s.mu.Lock()
			ok := user == "alice" && arg(0) == "secret" && !s.rejectAuth
			drop = slices.Clone(s.msgs)
			s.mu.Unlock()
			if !ok {
				drop = nil
				say("-ERR [AUTH] invalid credentials")
				continue
			}
			if drop == nil {
				drop = []pop3Msg{}
			}
			deleted = make([]bool, len(drop))
			say("+OK signed in")
		case !authed:
			say("-ERR sign in first")
		case cmd == "NOOP":
			say("+OK")
		case cmd == "RSET":
			clear(deleted)
			say("+OK")
		case cmd == "STAT":
			n, size := 0, 0
			for i, m := range drop {
				if !deleted[i] {
					n, size = n+1, size+len(m.raw)
				}
			}
			say(fmt.Sprintf("+OK %d %d", n, size))
		case cmd == "LIST" || cmd == "UIDL":
			s.mu.Lock()
			noUIDL := s.noUIDL
			s.mu.Unlock()
			if cmd == "UIDL" && noUIDL {
				say("-ERR command not supported")
				continue
			}
			item := func(i int) string {
				if cmd == "LIST" {
					return fmt.Sprintf("%d %d", i+1, len(drop[i].raw))
				}
				return fmt.Sprintf("%d %s", i+1, drop[i].uid)
			}
			if len(args) > 0 {
				if i := msg(arg(0)); i >= 0 {
					say("+OK " + item(i))
				} else {
					say("-ERR no such message")
				}
				continue
			}
			out := []string{"+OK"}
			for i := range drop {
				if !deleted[i] {
					out = append(out, item(i))
				}
			}
			say(append(out, ".")...)
		case cmd == "RETR" || cmd == "TOP":
			i := msg(arg(0))
			if i < 0 {
				say("-ERR no such message")
				continue
			}
			s.mu.Lock()
			if cmd == "TOP" && s.noTOP {
				s.mu.Unlock()
				say("-ERR command not supported")
				continue
			}
			fail := cmd == "RETR" && s.retrLeft == 0
			if cmd == "RETR" && s.retrLeft > 0 {
				s.retrLeft--
			}
			s.mu.Unlock()
			if fail {
				say("-ERR unable to read message")
				continue
			}
			raw := drop[i].raw
			if cmd == "TOP" {
				n, err := strconv.Atoi(arg(1))
				if err != nil || n < 0 {
					say("-ERR bad line count")
					continue
				}
				raw = top(raw, n)
			}
			out := []string{"+OK"}
			for l := range strings.Lines(strings.ReplaceAll(string(raw), "\r\n", "\n")) {
				l = strings.TrimSuffix(l, "\n")
				if strings.HasPrefix(l, ".") {
					l = "." + l
				}
				out = append(out, l)
			}
			say(append(out, ".")...)
		case cmd == "DELE":
			s.mu.Lock()
			refuse := s.rejectDele
			s.mu.Unlock()
			if i := msg(arg(0)); refuse {
				say("-ERR deleting is not allowed")
			} else if i >= 0 {
				deleted[i] = true
				say("+OK marked")
			} else {
				say("-ERR no such message")
			}
		default:
			say("-ERR unknown command")
		}
	}
}

// top is raw's header, the blank line and the first n lines of its body.
func top(raw []byte, n int) []byte {
	head, body, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !ok {
		return raw
	}
	out := append(append([]byte(nil), head...), "\r\n\r\n"...)
	for l := range bytes.Lines(body) {
		if n == 0 {
			break
		}
		out, n = append(out, l...), n-1
	}
	return out
}

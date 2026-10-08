package conn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"syscall"
)

// FailureKind is the broad cause of a failed dial, for diagnostics.
type FailureKind string

const (
	FailureDNS      FailureKind = "dns"
	FailureConnect  FailureKind = "connect"
	FailureTimeout  FailureKind = "timeout"
	FailureTLS      FailureKind = "tls"
	FailureAuth     FailureKind = "auth"
	FailureProtocol FailureKind = "protocol"
)

// Windows reports refused/unreachable connects as WinSock codes, which are not
// syscall.ECONNREFUSED etc. there.
const (
	wsaeNetUnreach  syscall.Errno = 10051
	wsaeConnRefused syscall.Errno = 10061
	wsaeHostUnreach syscall.Errno = 10065
)

// Classify maps an error from DialIMAP or DialSMTP to a FailureKind; nil gives "".
func Classify(err error) FailureKind {
	var (
		dnsErr *net.DNSError
		errno  syscall.Errno
		ne     net.Error
	)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAuthFailed):
		return FailureAuth
	case errors.As(err, &dnsErr):
		return FailureDNS
	case errors.As(err, &errno) && isConnectErrno(errno):
		return FailureConnect
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return FailureTimeout
	case isTLS(err):
		return FailureTLS
	}
	return FailureProtocol
}

func isConnectErrno(e syscall.Errno) bool {
	switch e {
	case syscall.ECONNREFUSED, syscall.EHOSTUNREACH, syscall.ENETUNREACH,
		wsaeConnRefused, wsaeHostUnreach, wsaeNetUnreach:
		return true
	}
	return false
}

func isTLS(err error) bool {
	var (
		rh    tls.RecordHeaderError
		alert tls.AlertError
		cv    *tls.CertificateVerificationError
		ua    x509.UnknownAuthorityError
		he    x509.HostnameError
		ci    x509.CertificateInvalidError
	)
	return errors.As(err, &rh) || errors.As(err, &alert) || errors.As(err, &cv) ||
		errors.As(err, &ua) || errors.As(err, &he) || errors.As(err, &ci)
}

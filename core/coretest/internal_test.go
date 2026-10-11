package coretest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fatalRecorder records Fatalf instead of stopping the test.
type fatalRecorder struct {
	testing.TB
	msg string
}

func (r *fatalRecorder) Fatalf(format string, args ...any) { r.msg = format }
func (r *fatalRecorder) Helper()                           {}

func TestOneLiveEnv(t *testing.T) {
	t.Run("first", func(t *testing.T) { New(t, WithoutAutoSync()) })
	New(t, WithoutAutoSync()) // the first one is closed: a new one starts
	r := &fatalRecorder{TB: t}
	claimEnv(r)
	assert.Contains(t, r.msg, "another Env is still alive")
}

// The fixture network reaches only loopback: a public name neither resolves
// nor, as an address, dials.
func TestNetworkClosed(t *testing.T) {
	opts := netOptions("127.0.0.1:1")
	c := &http.Client{Transport: plugins.NewClient(opts).Transport(), Timeout: 5 * time.Second}
	_, err := c.Get("http://example.com/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coretest")
	_, err = opts.Dial(context.Background(), "tcp", "93.184.216.34:80")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "coretest"), err.Error())
	ips, err := opts.Resolver.LookupIPAddr(context.Background(), "localhost")
	require.NoError(t, err)
	assert.True(t, ips[0].IP.IsLoopback())
}

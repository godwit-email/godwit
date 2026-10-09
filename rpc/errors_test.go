package rpc_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockedErr struct {
	Who  string
	Tags map[string]int64
}

func (e *blockedErr) Error() string { return "blocked by " + e.Who }

var errGone = errors.New("gone away")

func testCodec() *rpc.Codec {
	c := &rpc.Codec{}
	rpc.RegisterType(c, "blocked", func(e *blockedErr) any { return e }, rpc.DecodeDetail[blockedErr])
	c.RegisterSentinel("gone", errGone, "it is gone")
	return c
}

func roundTrip(t *testing.T, c *rpc.Codec, err error, redact func(string) string) (*rpc.WireError, error) {
	t.Helper()
	we := c.Encode(err, redact)
	b, merr := json.Marshal(we)
	require.NoError(t, merr)
	var back rpc.WireError
	require.NoError(t, json.Unmarshal(b, &back))
	return we, c.Decode(&back)
}

func TestCodecSentinel(t *testing.T) {
	c := testCodec()
	we, got := roundTrip(t, c, fmt.Errorf("op: %w", errGone), nil)
	assert.Equal(t, "it is gone", we.Message)
	assert.Equal(t, "gone", we.Data.Code)
	assert.Same(t, errGone, got)
}

func TestCodecTypeRedactsEveryString(t *testing.T) {
	c := testCodec()
	redact := func(s string) string { return strings.ReplaceAll(s, "hunter22", "[redacted]") }
	in := &blockedErr{Who: "x hunter22", Tags: map[string]int64{"key hunter22": 1 << 60}}
	we, got := roundTrip(t, c, fmt.Errorf("wrap with hunter22: %w", in), redact)
	b, _ := json.Marshal(we)
	assert.NotContains(t, string(b), "hunter22")
	var be *blockedErr
	require.ErrorAs(t, got, &be)
	assert.Equal(t, "x [redacted]", be.Who)
	assert.Equal(t, map[string]int64{"key [redacted]": 1 << 60}, be.Tags) // large ints keep precision
	assert.Equal(t, "blocked by x [redacted]", we.Message)                // the type's own message, not the wrapper's
}

// Types are tried before sentinels: a type wrapping a sentinel stays the
// type, and is still that sentinel (its cause) on the client.
func TestCodecTypesFirst(t *testing.T) {
	c := testCodec()
	we, got := roundTrip(t, c, &wrapsGone{blockedErr{Who: "a"}}, nil)
	assert.Equal(t, "gone", we.Data.Cause)
	var be *blockedErr
	require.ErrorAs(t, got, &be)
	assert.Equal(t, "a", be.Who)
	assert.ErrorIs(t, got, errGone)
	assert.Equal(t, "blocked by a", got.Error())

	we, got = roundTrip(t, c, &blockedErr{Who: "a"}, nil)
	assert.Empty(t, we.Data.Cause)
	assert.NotErrorIs(t, got, errGone)
	got = c.Decode(&rpc.WireError{Code: rpc.CodeApp, Data: &rpc.WireErrorData{Code: "blocked", Detail: json.RawMessage(`{"Who":"a","Tags":null}`), Cause: "newer"}})
	require.ErrorAs(t, got, &be, "an unknown cause is dropped")
	got = c.Decode(&rpc.WireError{Code: rpc.CodeApp, Data: &rpc.WireErrorData{Code: "blocked", Detail: json.RawMessage(`{}`), Cause: "gone"}})
	assert.ErrorIs(t, got, rpc.ErrBadErrorData)
	assert.NotErrorIs(t, got, errGone, "bad data never decodes to a business error")
}

type wrapsGone struct{ blockedErr }

func (w *wrapsGone) As(target any) bool {
	if p, ok := target.(**blockedErr); ok {
		*p = &w.blockedErr
		return true
	}
	return false
}
func (w *wrapsGone) Unwrap() error { return errGone }

func TestCodecUnknown(t *testing.T) {
	c := testCodec()
	we, got := roundTrip(t, c, errors.New("Dear Bob, the secret plan"), nil)
	assert.Equal(t, &rpc.WireError{Code: rpc.CodeInternal, Message: "internal error", Data: &rpc.WireErrorData{Code: "internal"}}, we)
	var re *rpc.RemoteError
	require.ErrorAs(t, got, &re)
	assert.Equal(t, rpc.RemoteError{Code: rpc.CodeInternal, Message: "internal error"}, *re)
	assert.Nil(t, c.Encode(nil, nil))
	assert.Equal(t, "too many requests", c.Decode(&rpc.WireError{Code: rpc.CodeTooMany, Message: "too many requests", Data: &rpc.WireErrorData{Code: "too_many_requests"}}).Error())
}

func TestCodecDuplicatePanics(t *testing.T) {
	c := testCodec()
	assert.Panics(t, func() { c.RegisterSentinel("gone", errors.New("x"), "x") })
}

// A type's detail that is missing, mistyped, partial or has unknown fields
// decodes to ErrBadErrorData, never a zero or partial business error.
func TestCodecBadDetail(t *testing.T) {
	c := testCodec()
	for name, detail := range map[string]string{
		"missing": ``, "null": `null`, "not object": `"x"`,
		"wrong type": `{"Who":1,"Tags":{}}`, "partial": `{"Who":"a"}`, "unknown field": `{"Who":"a","Tags":{},"X":1}`,
	} {
		got := c.Decode(&rpc.WireError{Code: rpc.CodeApp, Message: "blocked by a", Data: &rpc.WireErrorData{Code: "blocked", Detail: json.RawMessage(detail)}})
		assert.ErrorIs(t, got, rpc.ErrBadErrorData, name)
		var be *blockedErr
		assert.False(t, errors.As(got, &be), name)
	}
	got := c.Decode(&rpc.WireError{Code: rpc.CodeApp, Data: &rpc.WireErrorData{Code: "blocked", Detail: json.RawMessage(`{"Who":"a","Tags":null}`)}})
	var be *blockedErr
	require.ErrorAs(t, got, &be)
	assert.Equal(t, "a", be.Who)
}

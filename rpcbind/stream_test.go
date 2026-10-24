package rpcbind_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Ask or Assist whose client stops reading, with events backlogged, ends
// when the client cancels the stream, drops the connection, or falls
// rpc.StreamBuffer behind: the run (which hangs until its client goes) is
// cancelled, the next run of the conversation starts, and Close is prompt.
func TestWitStreamCancelledOverSocket(t *testing.T) {
	for _, method := range []string{"Ask", "Assist"} {
		for _, how := range []string{"cancel", "close", "overflow"} {
			t.Run(method+"/"+how, func(t *testing.T) {
				e := coretest.New(t, coretest.WithoutAutoSync())
				ctx := t.Context()
				l := e.LLM(t)
				defer l.Release()
				chunks := make([]string, rpc.StreamBuffer/2)
				if how == "overflow" {
					chunks = make([]string, 2*rpc.StreamBuffer)
				}
				for i := range chunks {
					chunks[i] = strings.Repeat("x", 200) + " "
				}
				l.Script(coretest.Turn{Text: chunks, Hang: true})
				var target int64
				if method == "Ask" {
					conv, err := e.Svc.NewConversation(ctx, "ask", 0)
					require.NoError(t, err)
					target = conv.ID
				} else {
					acc := e.AddAccount(t, "alice@example.com")
					d, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
					require.NoError(t, err)
					target = d.ID
				}
				start := func(input string) (<-chan core.WitEvent, error) {
					if method == "Ask" {
						return e.Svc.Ask(ctx, target, input)
					}
					return e.Svc.Assist(ctx, target, input)
				}

				c, err := rpc.Dial(listen(t, e.Svc), rpc.RoleFull, "rpcbind-test", rpcbind.NewCodec())
				require.NoError(t, err)
				defer c.Close()
				events, cancel, err := c.Stream(ctx, method, target, "hello")
				require.NoError(t, err)
				<-events // the run is going; the rest backs up unread
				// Its first event comes before its request: cancelled before the
				// request arrives, the run would leave the hanging turn to the next.
				require.Eventually(t, func() bool { return l.Requests() > 0 }, 10*time.Second, 5*time.Millisecond)
				time.Sleep(200 * time.Millisecond)
				switch how {
				case "cancel":
					_, err = start("meanwhile")
					require.ErrorIs(t, err, core.ErrRunActive)
					cancel()
				case "close":
					_, err = start("meanwhile")
					require.ErrorIs(t, err, core.ErrRunActive)
					c.Close()
				}

				var next <-chan core.WitEvent
				for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
					next, err = start("again") // the script is spent: "ok"
					if !errors.Is(err, core.ErrRunActive) || time.Now().After(deadline) {
						break
					}
				}
				require.NoError(t, err, "the cancelled run is still active")
				var last core.WitEvent
				for ev := range next {
					last = ev
				}
				assert.Equal(t, "done", last.Kind)

				closed := make(chan error, 1)
				go func() { closed <- e.Svc.Close() }()
				select {
				case <-closed:
				case <-time.After(10 * time.Second):
					t.Fatal("Service.Close waited on the dropped stream")
				}
			})
		}
	}
}

package mailsync

import (
	"context"
	"errors"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

// Idle waits on c, which has INBOX selected, until the server reports a change (a
// signal on updates), wake fires, max passes or ctx ends. A NOOP first collects
// changes the server queued since SELECT, so mail that arrived before IDLE began
// still ends the wait. When ctx ends the caller closes c, which also stops IDLE.
// byServer reports that a server update ended the wait.
func Idle(ctx context.Context, c *imapclient.Client, updates, wake <-chan struct{}, max time.Duration) (byServer bool, err error) {
	if err := c.Noop().Wait(); err != nil {
		return false, err
	}
	cmd, err := c.Idle()
	if err != nil {
		return false, err
	}
	t := time.NewTimer(max)
	defer t.Stop()
	select {
	case <-updates:
		byServer = true
	case <-wake:
	case <-t.C:
	case <-c.Closed():
		return false, errors.New("imap: connection lost during IDLE")
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if err := cmd.Close(); err != nil {
		return byServer, err
	}
	return byServer, cmd.Wait()
}

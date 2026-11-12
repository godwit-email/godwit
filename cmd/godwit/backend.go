package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/uiclient"
)

// uiStartedFlag marks the backend the interface starts (headless's uiStarted).
const uiStartedFlag = "ui-started"

// The exit codes of a backend that could not start, by which the interface
// that started it says why (startupFailure); any other failure exits 1.
const (
	exitAnotherOwner = 2 // another process owns the data directory
	exitLockFailed   = 3 // the owner lock could not be taken
	exitOpenDenied   = 4 // opening the data directory was not permitted
)

// backendWait bounds how long the interface waits for its backend to answer:
// opening the database may migrate it. ownerWait bounds it once a started
// backend found another owner, which is quitting or about to answer.
const backendWait, ownerWait = time.Minute, 10 * time.Second

// backendCmd is the backend the interface starts on dataDir: this
// executable, headless and marked as the interface's, outliving it
// (detachBackend). Tests swap it.
var backendCmd = func(dataDir string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "--headless", "--data-dir", dataDir, "--"+uiStartedFlag)
	detachBackend(cmd)
	return cmd, nil
}

// startupError is a backend that exited with its code before it answered.
type startupError int

func (e startupError) Error() string { return fmt.Sprintf("the backend exited with code %d", int(e)) }

// connectBackend dials dataDir's backend as the ui role: one already
// running, the user's --headless included, or else one it starts
// (backendCmd), waiting up to wait for it to answer. A started backend that
// finds another owner (one still quitting, or another interface's starting)
// is started again for up to ownerWait more; one that fails otherwise is its
// startupError at once.
func connectBackend(dataDir string, wait time.Duration) (*uiclient.Client, error) {
	deadline := time.Now().Add(wait)
	var exited chan error // the started backend's Wait; nil while none runs
	var last error = errors.New("the backend did not answer")
	for {
		c, err := uiclient.Dial(dataDir, "")
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, last
		}
		if exited == nil {
			cmd, err := backendCmd(dataDir)
			if err == nil {
				err = cmd.Start()
			}
			if err != nil {
				return nil, err
			}
			exited = make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
		}
		select {
		case err := <-exited:
			exited = nil
			code := 0
			if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
				code = ee.ExitCode()
			}
			if last = startupError(code); code != exitAnotherOwner {
				return nil, last
			}
			if d := time.Now().Add(ownerWait); d.Before(deadline) {
				deadline = d
			}
			time.Sleep(200 * time.Millisecond) // let the other owner finish starting or quitting
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// startupFailure is what the interface tells the user when connectBackend
// fails, as the GUI did when it opened the data directory itself, and its
// exit code. The message never carries err's text.
func startupFailure(err error) (msg string, code int) {
	var se startupError
	errors.As(err, &se)
	switch se {
	case exitAnotherOwner:
		return lockFailure(ownerlock.ErrAnotherOwner)
	case exitLockFailed:
		return lockFailure(err)
	case exitOpenDenied:
		return openFailure(fs.ErrPermission), 1
	}
	return openFailure(err), 1
}

// watchBackend calls lost once c's connection drops while the interface is
// not quitting: the backend went away under it.
func watchBackend(c *uiclient.Client, quitting *atomic.Bool, lost func()) {
	go func() {
		<-c.Done()
		if !quitting.Load() {
			lost()
		}
	}()
}

// stopBackend asks c's backend to stop as the interface quits, and waits a
// little for it to go so a relaunch finds it gone. A backend the user started,
// or one whose 后台收信 is 一直继续, refuses and keeps running.
func stopBackend(c *uiclient.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.RPC().Call(ctx, "StopUIBackend", nil); err == nil {
		select {
		case <-c.Done():
		case <-ctx.Done():
		}
	}
	c.Close()
}

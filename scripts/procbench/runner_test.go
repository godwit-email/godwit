package main

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseBenchLogAcrossPreAndPostSplitEvents(t *testing.T) {
	raw := strings.Join([]string{
		"bench_ctl 1000000000 pid=5001 addr=127.0.0.1:51001 token=tok1",
		"mail_ready 1180000000 pid=5001",
		"window_hidden 2000000000 pid=5001",
		"bench_ctl 5010000000 pid=5099 addr=127.0.0.1:51002 token=tok2",
		"mail_ready 5095000000 pid=5099",
	}, "\n") + "\n"

	st := parseBenchLog(raw)
	if st.LatestCtl.Addr != "127.0.0.1:51002" || st.LatestCtl.Token != "tok2" {
		t.Fatalf("LatestCtl = %+v", st.LatestCtl)
	}
	if len(st.MailReady) != 2 || st.MailReady[0] != 1180000000 || st.MailReady[1] != 5095000000 {
		t.Fatalf("MailReady = %v", st.MailReady)
	}
	if len(st.WindowHidden) != 1 || st.WindowHidden[0] != 2000000000 {
		t.Fatalf("WindowHidden = %v", st.WindowHidden)
	}
}

func TestTriggerReopenHandlesWarmAndPostSplitColdUI(t *testing.T) {
	// Case 1: Pre-split warm UI listener is still open -> receives "open", spawnUI is NOT called.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gotLine := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		s, _ := bufio.NewReader(conn).ReadString('\n')
		gotLine <- strings.TrimSpace(s)
	}()

	spawned := 0
	if err := triggerReopen(ctlEndpoint{Addr: ln.Addr().String(), Token: "secret123"}, func() error {
		spawned++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := <-gotLine; got != "secret123 open" {
		t.Fatalf("warm listener got %q, want %q", got, "secret123 open")
	}
	if spawned != 0 {
		t.Fatalf("spawned = %d on warm listener, want 0", spawned)
	}
	_ = ln.Close()

	// Case 2: Post-split UI process exited on window close -> listener is gone, falls back to spawnUI.
	if err := triggerReopen(ctlEndpoint{Addr: ln.Addr().String(), Token: "secret123"}, func() error {
		spawned++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if spawned != 1 {
		t.Fatalf("spawned = %d when listener closed, want 1", spawned)
	}
}

func TestMedians(t *testing.T) {
	d := medianDuration([]time.Duration{300 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond})
	if d != 200*time.Millisecond {
		t.Fatalf("medianDuration = %v, want 200ms", d)
	}
	u := medianUint64([]uint64{90, 10, 40})
	if u != 40 {
		t.Fatalf("medianUint64 = %d, want 40", u)
	}
}

func TestAppBundleFromBin(t *testing.T) {
	got, ok := appBundleFromBin("/tmp/dist/darwin-arm64/Godwit.app/Contents/MacOS/Godwit")
	if !ok || got != "/tmp/dist/darwin-arm64/Godwit.app" {
		t.Fatalf("appBundleFromBin = (%q, %v)", got, ok)
	}
	if _, ok := appBundleFromBin("/tmp/godwit"); ok {
		t.Fatal("expected plain binary not to match .app bundle")
	}
}

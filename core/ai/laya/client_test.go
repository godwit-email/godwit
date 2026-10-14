package laya

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// The test binary is the worker too: TestMain serves as one when
// LAYA_TEST_WORKER names a kind.
func TestMain(m *testing.M) {
	switch os.Getenv("LAYA_TEST_WORKER") {
	case "":
		os.Exit(m.Run())
	case "fake":
		fakeWorker()
	case "fail":
		fmt.Fprintln(os.Stderr, "the model is missing")
		json.NewEncoder(os.Stdout).Encode(hello{Error: "no model"})
		os.Exit(1)
	case "real":
		err := Serve(os.Stdin, os.Stdout, Config{Lib: os.Getenv("GODWIT_LAYA_ORT"), Model: os.Getenv("GODWIT_LAYA_MODEL"), Threads: 4})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(0)
}

// Fake requests: the first token says what the fake worker does.
const (
	tokCrash = 7 // write to stderr and exit
	tokHang  = 8 // never answer
	tokError = 9 // answer with an error
)

// fakeWorker answers each marker with the number of tokens, and the action
// logits with its pid, to tell workers apart.
func fakeWorker() {
	enc, dec := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
	enc.Encode(hello{Ready: true})
	for {
		var in Input
		if dec.Decode(&in) != nil {
			return
		}
		switch in.InputIDs[0] {
		case tokCrash:
			fmt.Fprintln(os.Stderr, "boom")
			os.Exit(3)
		case tokHang:
			select {}
		case tokError:
			enc.Encode(response{Error: "laya: refused"})
			continue
		}
		out := Output{Logits: make([]float32, len(in.MarkerPos)), ActLogits: [2]float32{float32(os.Getpid())}}
		for i := range out.Logits {
			out.Logits[i] = float32(len(in.InputIDs))
		}
		enc.Encode(response{Output: &out})
	}
}

func newClient(t *testing.T, kind string, idle time.Duration) *Client {
	t.Setenv("LAYA_TEST_WORKER", kind) // the worker inherits it
	c := &Client{Command: []string{os.Args[0], "-test.run=^$"}, Idle: idle}
	t.Cleanup(func() { c.Close() })
	return c
}

func question(first int64) Input {
	return Input{InputIDs: []int64{first, 5, 6, 7}, MarkerPos: []int64{1, 2}}
}

// pid runs a plain question and returns the pid of the worker that answered.
func pid(t *testing.T, c *Client) int {
	t.Helper()
	out, err := c.Run(t.Context(), question(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Logits) != 2 || out.Logits[0] != 4 {
		t.Fatalf("answer %+v", out)
	}
	return int(out.ActLogits[0])
}

func running(c *Client) bool {
	c.lock(context.Background())
	defer c.unlock()
	return c.w != nil
}

func TestClientKeepsWorkerUntilIdle(t *testing.T) {
	c := newClient(t, "fake", 300*time.Millisecond)
	first := pid(t, c)
	if again := pid(t, c); again != first {
		t.Fatalf("a busy worker was replaced: %d then %d", first, again)
	}
	deadline := time.Now().Add(5 * time.Second)
	for running(c) {
		if time.Now().After(deadline) {
			t.Fatal("the idle worker still runs")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if next := pid(t, c); next == first {
		t.Fatal("no new worker after the idle stop")
	}
}

func TestClientRestartsDeadWorker(t *testing.T) {
	c := newClient(t, "fake", time.Minute)
	first := pid(t, c)
	_, err := c.Run(t.Context(), question(tokCrash))
	if !errors.Is(err, ErrWorker) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("crash: %v", err)
	}
	if next := pid(t, c); next == first {
		t.Fatal("the dead worker's pid answered")
	}
}

func TestClientCancelKillsWorker(t *testing.T) {
	c := newClient(t, "fake", time.Minute)
	first := pid(t, c)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Run(ctx, question(tokHang)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hang: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancel took %s", d)
	}
	if running(c) {
		t.Fatal("the hung worker was kept")
	}
	if next := pid(t, c); next == first {
		t.Fatal("the hung worker answered")
	}
}

func TestClientAnswerErrorKeepsWorker(t *testing.T) {
	c := newClient(t, "fake", time.Minute)
	first := pid(t, c)
	if _, err := c.Run(t.Context(), question(tokError)); err == nil || errors.Is(err, ErrWorker) || err.Error() != "laya: refused" {
		t.Fatalf("error answer: %v", err)
	}
	if again := pid(t, c); again != first {
		t.Fatal("an error answer replaced the worker")
	}
}

func TestClientStartFailure(t *testing.T) {
	c := newClient(t, "fail", time.Minute)
	_, err := c.Run(t.Context(), question(1))
	if !errors.Is(err, ErrWorker) || !strings.Contains(err.Error(), "no model") || !strings.Contains(err.Error(), "the model is missing") {
		t.Fatalf("start: %v", err)
	}
}

func TestInputValidate(t *testing.T) {
	ids := func(n int) []int64 { return make([]int64, n) }
	for _, tc := range []struct {
		name string
		in   Input
		ok   bool
	}{
		{"one token", Input{InputIDs: ids(1), MarkerPos: []int64{0}}, true},
		{"full context", Input{InputIDs: ids(MaxTokens), MarkerPos: []int64{MaxTokens - 1}, QType: QNoul}, true},
		{"no tokens", Input{MarkerPos: []int64{0}}, false},
		{"over the context", Input{InputIDs: ids(MaxTokens + 1), MarkerPos: []int64{0}}, false},
		{"no markers", Input{InputIDs: ids(4)}, false},
		{"marker past the end", Input{InputIDs: ids(4), MarkerPos: []int64{4}}, false},
		{"negative marker", Input{InputIDs: ids(4), MarkerPos: []int64{-1}}, false},
		{"unknown type", Input{InputIDs: ids(4), MarkerPos: []int64{0}, QType: 3}, false},
	} {
		if err := tc.in.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// TestLiveParity runs the real model in a worker and compares its logits
// with ONNX Runtime 1.27's from Python (testdata/reference.json, for
// yehor-oleksiuk/laya-multilingual-onnx model_int8.onnx, sha256 22fdf8b5…,
// as it is or with its weights in external data). It runs only when both
// files are named:
//
//	GODWIT_LAYA_ORT=/abs/libonnxruntime.1.27.1.dylib GODWIT_LAYA_MODEL=/abs/model_int8.onnx go test ./core/ai/laya -run TestLiveParity -v
func TestLiveParity(t *testing.T) {
	if os.Getenv("GODWIT_LAYA_ORT") == "" || os.Getenv("GODWIT_LAYA_MODEL") == "" {
		t.Skip("GODWIT_LAYA_ORT and GODWIT_LAYA_MODEL not set")
	}
	raw, err := os.ReadFile("testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Cases []struct {
			Input Input
			Want  Output
		}
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, "real", time.Minute)
	for i, tc := range ref.Cases {
		got, err := c.Run(t.Context(), tc.Input)
		if err != nil {
			t.Fatal(err)
		}
		for j, w := range append(tc.Want.Logits, tc.Want.ActLogits[:]...) {
			g := append(got.Logits, got.ActLogits[:]...)[j]
			if math.Abs(float64(g-w)) > 1e-4 {
				t.Errorf("case %d logit %d: %v, want %v", i, j, g, w)
			}
		}
	}
}

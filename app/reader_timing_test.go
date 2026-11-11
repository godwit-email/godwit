package app

import (
	"strings"
	"testing"
	"time"
)

func TestBodyTimingLogsOnceAtFirstHeight(t *testing.T) {
	defer func(on bool) { readerTiming = on }(readerTiming)
	readerTiming = true
	var bt bodyTiming
	if line := bt.shown(1); line != "" {
		t.Fatalf("before any page: %q", line)
	}
	bt.begin()
	bt.readDone()
	made := time.Now()
	bt.viewMade(made)
	bt.builtDone()
	line := bt.shown(7)
	for _, want := range []string{"message 7 shown in ", "read ", "view ", "build ", "paint "} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q lacks %q", line, want)
		}
	}
	if again := bt.shown(7); again != "" {
		t.Fatalf("logged twice: %q", again)
	}
	// Refolding makes a new page: it is timed again, from its making.
	bt.viewMade(time.Now())
	if line := bt.shown(7); !strings.Contains(line, "wait 0s") {
		t.Fatalf("remade page: %q", line)
	}
}

func TestBodyTimingOff(t *testing.T) {
	defer func(on bool) { readerTiming = on }(readerTiming)
	readerTiming = false
	var bt bodyTiming
	bt.begin()
	bt.readDone()
	bt.viewMade(time.Now())
	if line := bt.shown(1); line != "" {
		t.Fatalf("off, but logged %q", line)
	}
}

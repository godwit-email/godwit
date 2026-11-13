package rpc

import (
	"encoding/json"
	"strconv"
	"testing"
)

// A dropping stream whose reader takes events while older ones are dropped
// still hands them over in order, the newest last.
func TestStreamDroppingKeepsOrder(t *testing.T) {
	c := &Client{streams: map[uint64]*clientStream{}, errs: map[<-chan json.RawMessage]error{}}
	c.mu.Lock()
	s := c.newStream(1, func(json.RawMessage) bool { return true })
	c.mu.Unlock()
	const n = 100000
	done := make(chan [2]int)
	go func() {
		last, bad := -1, 0
		for b := range s.ch {
			v, _ := strconv.Atoi(string(b))
			if v < last {
				bad++
			}
			last = v
		}
		done <- [2]int{last, bad}
	}()
	for i := range n {
		c.mu.Lock()
		ok := c.pushLocked(s, json.RawMessage(strconv.Itoa(i)))
		c.mu.Unlock()
		if !ok {
			t.Fatalf("event %d overflowed a stream that may drop any", i)
		}
	}
	c.mu.Lock()
	c.endLocked(s, nil)
	c.mu.Unlock()
	if r := <-done; r[1] > 0 || r[0] != n-1 {
		t.Fatalf("%d events arrived after a later one; last %d, want %d", r[1], r[0], n-1)
	}
}

package testclock_test

import (
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testclock"
	"github.com/stretchr/testify/assert"
)

func TestAdvance(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := testclock.New(start)
	assert.Equal(t, start, c.Now())
	var fired []string
	c.AfterFunc(2*time.Second, func() { fired = append(fired, "2s") })
	c.AfterFunc(time.Second, func() { fired = append(fired, "1s") })
	stop := c.AfterFunc(time.Second, func() { fired = append(fired, "stopped") })
	assert.True(t, stop(), "stopping a pending timer reports it")

	c.Advance(999 * time.Millisecond)
	assert.Empty(t, fired, "nothing is due yet")
	c.Advance(time.Millisecond)
	assert.Equal(t, []string{"1s"}, fired)
	assert.Equal(t, start.Add(time.Second), c.Now())
	c.Advance(time.Hour)
	assert.Equal(t, []string{"1s", "2s"}, fired, "each fires once, and a stopped one never")
	assert.False(t, stop(), "a stopped timer is not pending")
}

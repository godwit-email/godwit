package app

import (
	"strings"
	"sync"
)

// recentLogMax is how many lines RecentLog keeps.
const recentLogMax = 100

// RecentLog keeps the log's last lines in memory, never on disk, for an
// error report to carry; the GUI tees the standard logger into it.
var RecentLog = &logRing{}

type logRing struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for l := range strings.Lines(string(p)) {
		r.lines = append(r.lines, strings.TrimSuffix(l, "\n"))
	}
	if n := len(r.lines) - recentLogMax; n > 0 {
		r.lines = r.lines[n:]
	}
	return len(p), nil
}

func (r *logRing) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

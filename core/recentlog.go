package core

import (
	"strings"
	"sync"
)

// RecentLogMax is how many lines RecentLog keeps.
const RecentLogMax = 100

// RecentLog keeps the process's last log lines in memory, never on disk, for
// a bug report to carry redacted.
var RecentLog = &LogRing{}

// LogRing is a thread-safe in-memory ring of recent log lines.
type LogRing struct {
	mu    sync.Mutex
	lines []string
}

func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for l := range strings.Lines(string(p)) {
		r.lines = append(r.lines, strings.TrimSuffix(l, "\n"))
	}
	if n := len(r.lines) - RecentLogMax; n > 0 {
		r.lines = r.lines[n:]
	}
	return len(p), nil
}

func (r *LogRing) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// RecentLog returns the recent process log lines with every credential and
// extension secret this Service knows of replaced by "[redacted]".
func (s *Service) RecentLog() string {
	if s.opts.RecentLog != nil {
		return s.Redact(s.opts.RecentLog())
	}
	return s.Redact(RecentLog.String())
}

package app

import "github.com/godwit-email/godwit/core"

// recentLogMax is how many lines RecentLog keeps.
const recentLogMax = core.RecentLogMax

// RecentLog keeps the log's last lines in memory, never on disk, for an
// error report to carry; the backend tees its log into it.
var RecentLog = core.RecentLog

type logRing = core.LogRing

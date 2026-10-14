package main

import (
	"fmt"
	"io"

	"github.com/godwit-email/godwit/core/ai/laya"
)

// layaWorker is the local decision model's worker process (laya.Serve), which
// a laya.Client starts; it is not for people, so usage leaves it out.
func layaWorker(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("godwit laya-worker", stderr)
	var cfg laya.Config
	fs.StringVar(&cfg.Lib, "lib", "", "")
	fs.StringVar(&cfg.Model, "model", "", "")
	fs.IntVar(&cfg.Threads, "threads", 4, "")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if err := laya.Serve(stdin, stdout, cfg); err != nil {
		fmt.Fprintln(stderr, "godwit laya-worker:", err)
		return 1
	}
	return 0
}

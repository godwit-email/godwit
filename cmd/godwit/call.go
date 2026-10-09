package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/rpc"
)

// call runs `godwit call [--data-dir DIR] METHOD [JSON_ARGS_ARRAY]` as the
// full role: a result is printed as JSON, a stream's events one per line
// until it ends or ctx does. An error prints "code: message" and exits 1.
func call(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("godwit call", stderr)
	dataDir := fs.String("data-dir", "", "")
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fs.Usage()
		return 2
	}
	method := fs.Arg(0)
	var params []json.RawMessage
	if fs.NArg() == 2 {
		if err := json.Unmarshal([]byte(fs.Arg(1)), &params); err != nil {
			fmt.Fprintln(stderr, "godwit call: the arguments must be a JSON array:", err)
			return 2
		}
	}
	argv := make([]any, len(params))
	for i, p := range params {
		argv[i] = p
	}
	dir, err := dataDirOr(*dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "godwit:", err)
		return 1
	}
	c, err := rpc.Dial(dir, rpc.RoleFull, "godwit-call", nil) // no codec: errors keep their wire code
	if err != nil {
		return printErr(stderr, err)
	}
	defer c.Close()

	if !isStream(method) {
		var res json.RawMessage
		if err := c.Call(ctx, method, &res, argv...); err != nil {
			return printErr(stderr, err)
		}
		fmt.Fprintf(stdout, "%s\n", res)
		return 0
	}
	events, cancel, err := c.Stream(ctx, method, argv...)
	if err != nil {
		return printErr(stderr, err)
	}
	defer cancel()
	for ev := range events {
		fmt.Fprintf(stdout, "%s\n", ev)
	}
	if err := c.StreamErr(events); err != nil {
		return printErr(stderr, err)
	}
	return 0
}

// isStream: method returns a channel on *core.Service.
func isStream(method string) bool {
	m, ok := reflect.TypeFor[*core.Service]().MethodByName(method)
	return ok && m.Type.NumOut() > 0 && m.Type.Out(0).Kind() == reflect.Chan
}

// printErr prints "code: message" (a registered error's stable code, else
// the JSON-RPC code) and returns 1.
func printErr(stderr io.Writer, err error) int {
	code := "error"
	var we *rpc.WireError
	if errors.As(err, &we) {
		code = strconv.Itoa(we.Code)
		if we.Data != nil && we.Data.Code != "" {
			code = we.Data.Code
		}
	}
	fmt.Fprintf(stderr, "%s: %s\n", code, err.Error())
	return 1
}

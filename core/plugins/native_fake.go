package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// FakeNativeOpener is a NativeOpener for tests that loads nothing: the
// "library" file is JSON {"describe": {...}, "initFail": bool, "echoStderr":
// "...", "record": "<path>"}. initFail writes echoStderr and fails init.
// Methods: describe, configure (writes its request to record, if set),
// translate (echoes the texts, source "en"), fail (a rate_limited envelope),
// probe.sleep {"ms"} and probe.crash (writes echoStderr, exits 2).
func FakeNativeOpener(stderr io.Writer) NativeOpener {
	return func(path string, hostABI uint32) (NativeCall, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var spec struct {
			Describe   json.RawMessage `json:"describe"`
			InitFail   bool            `json:"initFail"`
			EchoStderr string          `json:"echoStderr"`
			Record     string          `json:"record"`
		}
		if err := json.Unmarshal(b, &spec); err != nil {
			return nil, fmt.Errorf("fake library: %w", err)
		}
		if spec.InitFail || hostABI != NativeABI {
			fmt.Fprintln(stderr, spec.EchoStderr)
			return nil, errors.New("incompatible ABI (fake init failed)")
		}
		ok := func(v any) []byte {
			b, _ := json.Marshal(map[string]any{"ok": true, "result": v})
			return b
		}
		return func(method string, req []byte) ([]byte, error) {
			switch method {
			case "describe":
				return ok(spec.Describe), nil
			case "configure":
				if spec.Record != "" {
					if err := os.WriteFile(spec.Record, req, 0o600); err != nil {
						return nil, err
					}
				}
				return ok(struct{}{}), nil
			case "translate":
				var r struct {
					Texts []string `json:"texts"`
				}
				json.Unmarshal(req, &r)
				return ok(map[string]any{"texts": r.Texts, "source": "en"}), nil
			case "fail":
				return []byte(`{"ok":false,"error":{"code":"rate_limited","message":"slow down","retryable":true}}`), nil
			case "probe.sleep":
				var r struct {
					MS int `json:"ms"`
				}
				json.Unmarshal(req, &r)
				time.Sleep(time.Duration(r.MS) * time.Millisecond)
				return ok(struct{}{}), nil
			case "probe.crash":
				fmt.Fprintln(stderr, spec.EchoStderr)
				os.Exit(2)
			}
			return []byte(`{"ok":false,"error":{"code":"invalid_request","message":"unknown method"}}`), nil
		}, nil
	}
}

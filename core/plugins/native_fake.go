package plugins

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// FakeNativeOpener is a NativeOpener for tests that loads nothing: the
// "library" file is JSON {"describe": {...}, "initFail": bool, "echoStderr":
// "...", "record": "<path>", "describeSleepMs": n, "recordEnv": bool,
// "source": "en", "translateWrongCount": bool, "translateError": "<code>"}.
// initFail writes echoStderr and fails init; describe answers after
// describeSleepMs. translate answers "[<target>] <text>" per text from source
// (default "en"), one text short with translateWrongCount, or the error
// envelope translateError; with record each request is appended to
// record + ".translate", and recordEnv adds the environment to configure's record.
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
			Describe        json.RawMessage `json:"describe"`
			RestartDescribe json.RawMessage `json:"restartDescribe"`
			DescribeSleepMs int             `json:"describeSleepMs"`
			InitFail        bool            `json:"initFail"`
			EchoStderr      string          `json:"echoStderr"`
			Record          string          `json:"record"`
			RecordEnv       bool            `json:"recordEnv"`
			Source          string          `json:"source"`
			WrongCount      bool            `json:"translateWrongCount"`
			TranslateError  string          `json:"translateError"`
		}
		if err := json.Unmarshal(b, &spec); err != nil {
			return nil, fmt.Errorf("fake library: %w", err)
		}
		if spec.Record != "" && spec.RestartDescribe != nil { // a later process describes itself otherwise
			starts := spec.Record + ".starts"
			if _, err := os.Stat(starts); err == nil {
				spec.Describe = spec.RestartDescribe
			}
			os.WriteFile(starts, []byte("started\n"), 0o600)
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
				time.Sleep(time.Duration(spec.DescribeSleepMs) * time.Millisecond)
				return ok(spec.Describe), nil
			case "configure":
				if spec.Record != "" {
					rec := req
					if spec.RecordEnv {
						var r map[string]any
						json.Unmarshal(req, &r)
						r["env"] = os.Environ()
						rec, _ = json.Marshal(r)
					}
					if err := os.WriteFile(spec.Record, rec, 0o600); err != nil {
						return nil, err
					}
				}
				return ok(struct{}{}), nil
			case "translate":
				if spec.Record != "" {
					f, err := os.OpenFile(spec.Record+".translate", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
					if err != nil {
						return nil, err
					}
					f.Write(append(append([]byte{}, req...), '\n'))
					f.Close()
				}
				if spec.TranslateError != "" {
					return []byte(`{"ok":false,"error":{"code":"` + spec.TranslateError + `","message":"slow down","retryable":true}}`), nil
				}
				var r struct {
					Texts  []string `json:"texts"`
					Target string   `json:"target"`
				}
				json.Unmarshal(req, &r)
				out := make([]string, 0, len(r.Texts))
				for _, t := range r.Texts {
					out = append(out, "["+r.Target+"] "+t)
				}
				if spec.WrongCount && len(out) > 0 {
					out = out[1:]
				}
				return ok(map[string]any{"texts": out, "source": cmp.Or(spec.Source, "en")}), nil
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

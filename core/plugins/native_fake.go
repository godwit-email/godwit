package plugins

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
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
			Models          json.RawMessage `json:"models"`
			ModelsAfterList json.RawMessage `json:"modelsAfterList"`
			ModelAnswers    json.RawMessage `json:"modelAnswers"`
			ModelError      string          `json:"modelError"`
			ModelsSleepMs   int             `json:"modelsSleepMs"`
			ModelLoadFailAt map[string]int  `json:"modelLoadFailAt"`
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
		loadCalls := map[string]int{}
		listCalls := 0
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
			case "models.list", "models.prepare", "models.load", "models.judge":
				if spec.Record != "" {
					f, err := os.OpenFile(spec.Record+"."+method, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
					if err != nil {
						return nil, err
					}
					_, err = f.Write(append(append([]byte{}, req...), '\n'))
					cerr := f.Close()
					if err != nil || cerr != nil {
						return nil, errors.Join(err, cerr)
					}
				}
				if method == "models.load" {
					var r ModelLoadRequest
					json.Unmarshal(req, &r)
					loadCalls[r.ModelID]++
					if at := spec.ModelLoadFailAt[r.ModelID]; at > 0 && loadCalls[r.ModelID] >= at {
						return []byte(`{"ok":false,"error":{"code":"model_unavailable","message":"load failed","retryable":true}}`), nil
					}
				}
				time.Sleep(time.Duration(spec.ModelsSleepMs) * time.Millisecond)
				if spec.ModelError != "" {
					b, _ := json.Marshal(map[string]any{"ok": false, "error": NativeError{Code: spec.ModelError, Message: "model unavailable", Retryable: true}})
					return b, nil
				}
				switch method {
				case "models.list":
					models := spec.Models
					listCalls++
					if listCalls > 1 && spec.ModelsAfterList != nil {
						models = spec.ModelsAfterList
					}
					if models == nil {
						models = json.RawMessage(`[]`)
					}
					return ok(map[string]any{"models": models}), nil
				case "models.prepare":
					var r PrepareModelRequest
					if err := json.Unmarshal(req, &r); err != nil {
						return nil, err
					}
					return ok(r.Request), nil
				case "models.judge":
					answers := spec.ModelAnswers
					if answers == nil {
						answers = json.RawMessage(`[]`)
					}
					return ok(map[string]any{"answers": answers}), nil
				default:
					return ok(struct{}{}), nil
				}
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
			case "probe.spawn": // a helper process, its pid in record + ".helper"
				helper := exec.Command("sleep", "30")
				if err := helper.Start(); err != nil {
					return nil, err
				}
				os.WriteFile(spec.Record+".helper", []byte(strconv.Itoa(helper.Process.Pid)), 0o600)
				return ok(struct{}{}), nil
			case "probe.crash":
				fmt.Fprintln(stderr, spec.EchoStderr)
				os.Exit(2)
			}
			return []byte(`{"ok":false,"error":{"code":"invalid_request","message":"unknown method"}}`), nil
		}, nil
	}
}

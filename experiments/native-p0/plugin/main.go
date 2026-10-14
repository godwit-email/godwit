// P0 experiment: DLX behind the design's ABI v1 (describe, configure, translate)
// plus probe.* methods for crash and timeout tests. Throwaway.
package main

/*
#cgo CFLAGS: -I..
#include <stdlib.h>
#include "abi.h"
void fill_api(godwit_plugin_api *api);
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unsafe"

	"github.com/OwO-Network/DLX/translate"
)

type pluginError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

func (e *pluginError) Error() string { return e.Message }

func strict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &pluginError{Code: "invalid_request", Message: err.Error()}
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return &pluginError{Code: "invalid_request", Message: "expected one JSON object"}
	}
	return nil
}

// deeplLang maps a BCP 47 tag to DLX's code: zh-CN → ZH, en-US → EN-US, pt-BR → PT-BR.
func deeplLang(tag string) string {
	up := strings.ToUpper(tag)
	if base, _, ok := strings.Cut(up, "-"); ok && base == "ZH" {
		return "ZH"
	}
	return up
}

func dispatch(method string, raw []byte) (any, error) {
	switch method {
	case "describe":
		return map[string]any{"name": "dlx", "version": "0.0.0-p0", "abi": 1, "capabilities": []string{"translate"}}, nil
	case "configure":
		var req struct {
			Config map[string]any `json:"config"`
		}
		return struct{}{}, strict(raw, &req)
	case "translate":
		var req struct {
			Texts  []string `json:"texts"`
			Source string   `json:"source"`
			Target string   `json:"target"`
		}
		if err := strict(raw, &req); err != nil {
			return nil, err
		}
		res, err := translate.TranslateByDLX(deeplLang(req.Source), deeplLang(req.Target), req.Texts, "", "", "")
		if err != nil {
			return nil, &pluginError{Code: "upstream", Message: err.Error(), Retryable: true}
		}
		switch {
		case res.Code == 200:
			return map[string]any{"texts": res.Data, "source": strings.ToLower(res.SourceLang)}, nil
		case res.Code == 429:
			return nil, &pluginError{Code: "rate_limited", Message: res.Message, Retryable: true}
		case res.Code == 400 || res.Code == 413:
			return nil, &pluginError{Code: "invalid_request", Message: res.Message}
		default:
			return nil, &pluginError{Code: "upstream", Message: fmt.Sprintf("%d %s", res.Code, res.Message), Retryable: true}
		}
	case "probe.sleep":
		var req struct {
			MS int `json:"ms"`
		}
		if err := strict(raw, &req); err != nil {
			return nil, err
		}
		time.Sleep(time.Duration(req.MS) * time.Millisecond)
		return struct{}{}, nil
	case "probe.crash":
		go func() { panic("probe.crash: unrecovered panic on another goroutine") }()
		time.Sleep(time.Second)
		return struct{}{}, nil
	}
	return nil, &pluginError{Code: "invalid_request", Message: fmt.Sprintf("unknown method %q", method)}
}

func respond(out *C.godwit_buffer, result any, err error) C.int {
	env := map[string]any{"ok": err == nil}
	if err != nil {
		var pe *pluginError
		if !errors.As(err, &pe) {
			pe = &pluginError{Code: "internal", Message: err.Error()}
		}
		env["error"] = pe
	} else {
		env["result"] = result
	}
	raw, mErr := json.Marshal(env)
	if mErr != nil {
		return -1
	}
	out.ptr = C.CBytes(raw)
	if out.ptr == nil {
		return -1
	}
	out.len = C.size_t(len(raw))
	return 0
}

//export godwit_plugin_init
func godwit_plugin_init(hostABI C.uint32_t, api *C.godwit_plugin_api) C.int {
	if hostABI != C.GODWIT_PLUGIN_ABI || api == nil {
		return -1
	}
	C.fill_api(api)
	return 0
}

//export godwit_plugin_call
func godwit_plugin_call(method *C.char, req *C.uint8_t, size C.size_t, out *C.godwit_buffer) (rc C.int) {
	if out == nil {
		return -1
	}
	*out = C.godwit_buffer{}
	defer func() {
		if r := recover(); r != nil {
			rc = respond(out, nil, &pluginError{Code: "internal", Message: fmt.Sprintf("plugin panic: %v", r)})
		}
	}()
	if method == nil || size > 1<<20 || (size != 0 && req == nil) {
		return respond(out, nil, &pluginError{Code: "invalid_request", Message: "invalid ABI request"})
	}
	result, err := dispatch(C.GoString(method), C.GoBytes(unsafe.Pointer(req), C.int(size)))
	return respond(out, result, err)
}

//export godwit_plugin_free
func godwit_plugin_free(ptr unsafe.Pointer, size C.size_t) { C.free(ptr) }

func main() {}

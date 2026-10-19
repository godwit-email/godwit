package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
)

// systemTranslateLib is the bridge native/systemtranslate builds into the
// app's resources (scripts/build-system-translate.sh).
const systemTranslateLib = "libgodwit-translate.dylib"

// macTranslator is macOS's Translation framework through that bridge.
type macTranslator struct {
	translate func(req *byte) unsafe.Pointer
	free      func(unsafe.Pointer)
	version   string
}

// systemTranslator is the Mac's translation, nil before macOS 26 or without
// the bridge (a build that skipped it), when Godwit offers none.
var systemTranslator = sync.OnceValue(func() core.SystemTranslator {
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return nil
	}
	lib, err := purego.Dlopen(filepath.Join(dir, systemTranslateLib), purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil
	}
	var available func() int32
	t := &macTranslator{}
	purego.RegisterLibFunc(&available, lib, "godwit_st_available")
	if available() != 1 {
		return nil
	}
	purego.RegisterLibFunc(&t.translate, lib, "godwit_st_translate")
	purego.RegisterLibFunc(&t.free, lib, "godwit_st_free")
	t.version, _ = syscall.Sysctl("kern.osversion") // the build: an update may change the translations
	return t
})

func (t *macTranslator) Version() string { return t.version }

func (t *macTranslator) Translate(ctx context.Context, texts []string, source, target string) ([]string, string, error) {
	req, err := json.Marshal(map[string]any{"texts": texts, "source": source, "target": target})
	if err != nil {
		return nil, "", err
	}
	type answer struct {
		Texts  []string `json:"texts"`
		Source string   `json:"source"`
		Error  *struct {
			Code, Message string
		} `json:"error"`
	}
	done := make(chan answer, 1)
	go func() { // the bridge blocks and cannot be cancelled: a cancelled call finishes unseen
		p := t.translate(&append(req, 0)[0])
		defer t.free(p)
		var a answer
		if err := json.Unmarshal([]byte(goString(p)), &a); err != nil {
			a.Error = &struct{ Code, Message string }{"internal", "the answer is not JSON"}
		}
		done <- a
	}()
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case a := <-done:
		if a.Error == nil {
			return a.Texts, a.Source, nil
		}
		switch a.Error.Code {
		case "not_installed":
			return nil, "", fmt.Errorf("%w: %s", core.ErrSystemLanguageNotInstalled, a.Error.Message)
		case "unsupported_language":
			return nil, "", fmt.Errorf("%w: %s", core.ErrSystemLanguageUnsupported, a.Error.Message)
		}
		return nil, "", errors.New(a.Error.Message)
	}
}

// goString copies the NUL-terminated C string at p.
func goString(p unsafe.Pointer) string {
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

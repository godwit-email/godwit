package guard

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// CoreMLLibrary is the path of the bridge native/guardcoreml builds
// (libgodwit-guard-coreml.dylib); "" leaves the model on the CPU. Set it
// before the first Load.
var CoreMLLibrary string

// The bridge's functions; a string they return is malloc'd, and NULL is
// no error.
var ml struct {
	compile func(pkg, out string) unsafe.Pointer
	open    func(path string, err *unsafe.Pointer) unsafe.Pointer
	device  func(path string) unsafe.Pointer
	run     func(h unsafe.Pointer, x, mask *float32, s, hid int, out *float32, k int) unsafe.Pointer
	close   func(h unsafe.Pointer)
	free    func(p unsafe.Pointer)
}

var loadML = sync.OnceValue(func() error {
	if CoreMLLibrary == "" {
		return errors.New("no CoreML bridge")
	}
	lib, err := purego.Dlopen(CoreMLLibrary, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&ml.compile, lib, "godwit_ml_compile")
	purego.RegisterLibFunc(&ml.open, lib, "godwit_ml_open")
	purego.RegisterLibFunc(&ml.device, lib, "godwit_ml_device")
	purego.RegisterLibFunc(&ml.run, lib, "godwit_ml_run")
	purego.RegisterLibFunc(&ml.close, lib, "godwit_ml_close")
	purego.RegisterLibFunc(&ml.free, lib, "godwit_ml_free")
	return nil
})

// mlString is the bridge's string at p, freed; "" for NULL.
func mlString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	defer ml.free(p)
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

func mlErr(p unsafe.Pointer) error {
	if s := mlString(p); s != "" {
		return errors.New(s)
	}
	return nil
}

// mlCompile compiles the package at pkg into out (a .mlmodelc).
func mlCompile(pkg, out string) error { return mlErr(ml.compile(pkg, out)) }

// mlModel is a compiled program, opened for whichever compute unit CoreML
// picks; Device is the one its plan puts most of it on, or why that isn't
// known. It is let go with the last reference.
type mlModel struct {
	mu     sync.Mutex
	h      unsafe.Pointer
	Device string
}

func mlOpen(path string) (*mlModel, error) {
	var e unsafe.Pointer
	h := ml.open(path, &e)
	if h == nil {
		return nil, mlErr(e)
	}
	m := &mlModel{h: h, Device: mlString(ml.device(path))}
	runtime.AddCleanup(m, func(h unsafe.Pointer) { ml.close(h) }, h)
	return m, nil
}

// Run is the logits [s, k] for x [s, h] and mask [s].
func (m *mlModel) Run(x, mask []float32, s, h, k int) ([]float32, error) {
	if len(x) != s*h || len(mask) != s {
		return nil, errors.New("inputs of another size")
	}
	out := make([]float32, s*k)
	m.mu.Lock()
	err := mlErr(ml.run(m.h, &x[0], &mask[0], s, h, &out[0], k))
	m.mu.Unlock()
	runtime.KeepAlive(m)
	return out, err
}

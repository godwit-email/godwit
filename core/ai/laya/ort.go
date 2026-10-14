package laya

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ORTVersion is the ONNX Runtime the binding is written against and the only
// one it loads: another release changes the logits (up to 0.22 from 1.27 to
// 1.31 on the same input), and the slots below are its C API's.
const ORTVersion = "1.27.1"

// ortAPIVersion is ORT_API_VERSION of ORTVersion's onnxruntime_c_api.h.
const ortAPIVersion = 27

// Slots of struct OrtApi in ORTVersion's onnxruntime_c_api.h: the order of
// its members, counted from 0.
const (
	slotGetErrorMessage                  = 2
	slotCreateEnv                        = 3
	slotCreateSession                    = 7
	slotRun                              = 9
	slotCreateSessionOptions             = 10
	slotDisableMemPattern                = 17
	slotDisableCpuMemArena               = 19
	slotSetSessionGraphOptimizationLevel = 23
	slotSetIntraOpNumThreads             = 24
	slotCreateTensorWithDataAsOrtValue   = 49
	slotGetTensorMutableData             = 51
	slotCreateCpuMemoryInfo              = 69
	slotReleaseEnv                       = 92
	slotReleaseStatus                    = 93
	slotReleaseMemoryInfo                = 94
	slotReleaseSession                   = 95
	slotReleaseValue                     = 96
	slotReleaseSessionOptions            = 100
)

// ONNXTensorElementDataType values.
const (
	elemFloat = 1
	elemInt64 = 7
	elemBool  = 9
)

const (
	logWarning     = 2  // OrtLoggingLevel ORT_LOGGING_LEVEL_WARNING
	optimizeAll    = 99 // GraphOptimizationLevel ORT_ENABLE_ALL
	arenaAllocator = 1  // OrtAllocatorType OrtArenaAllocator
	memTypeDefault = 0  // OrtMemType OrtMemTypeDefault
)

var (
	inputNames  = cstrings("input_ids", "attention_mask", "marker_pos", "marker_mask", "qtype")
	outputNames = cstrings("logits", "act_logits")
)

// session is a Laya graph in ONNX Runtime. It is not safe for concurrent use.
type session struct {
	api                   uintptr // const OrtApi*
	env, opts, sess, info uintptr
}

// openSession loads the ONNX Runtime library at lib, which must be
// ORTVersion, and the graph at model, run on threads intra-op threads. The
// arena and memory patterns are off: they keep the high-water mark of the
// longest input for the session's life.
func openSession(lib, model string, threads int) (s *session, err error) {
	h, err := openLibrary(lib)
	if err != nil {
		return nil, err
	}
	sym, err := librarySymbol(h, "OrtGetApiBase")
	if err != nil {
		return nil, err
	}
	base, _, _ := purego.SyscallN(sym)
	if base == 0 {
		return nil, errors.New("laya: OrtGetApiBase returned NULL")
	}
	baseFns := unsafe.Slice((*uintptr)(cptr(base)), 2) // GetApi, GetVersionString
	verPtr, _, _ := purego.SyscallN(baseFns[1])
	if v := gostring(verPtr); v != ORTVersion {
		return nil, fmt.Errorf("laya: ONNX Runtime %s at %s, want %s", v, lib, ORTVersion)
	}
	s = &session{}
	if s.api, _, _ = purego.SyscallN(baseFns[0], ortAPIVersion); s.api == 0 {
		return nil, fmt.Errorf("laya: ONNX Runtime has no C API %d", ortAPIVersion)
	}
	defer func() {
		if err != nil {
			s.close()
		}
	}()
	logID := cstring("laya")
	if err := s.call(slotCreateEnv, logWarning, uintptr(unsafe.Pointer(logID)), uintptr(unsafe.Pointer(&s.env))); err != nil {
		return nil, err
	}
	if err := s.call(slotCreateSessionOptions, uintptr(unsafe.Pointer(&s.opts))); err != nil {
		return nil, err
	}
	for _, c := range []struct {
		slot int
		args []uintptr
	}{
		{slotSetIntraOpNumThreads, []uintptr{s.opts, uintptr(threads)}},
		{slotSetSessionGraphOptimizationLevel, []uintptr{s.opts, optimizeAll}},
		{slotDisableCpuMemArena, []uintptr{s.opts}},
		{slotDisableMemPattern, []uintptr{s.opts}},
	} {
		if err := s.call(c.slot, c.args...); err != nil {
			return nil, err
		}
	}
	path := ortPath(model)
	if err := s.call(slotCreateSession, s.env, uintptr(path), s.opts, uintptr(unsafe.Pointer(&s.sess))); err != nil {
		return nil, err
	}
	runtime.KeepAlive(logID)
	runtime.KeepAlive(path)
	if err := s.call(slotCreateCpuMemoryInfo, arenaAllocator, memTypeDefault, uintptr(unsafe.Pointer(&s.info))); err != nil {
		return nil, err
	}
	return s, nil
}

// run runs one question; in is valid.
func (s *session) run(in Input) (Output, error) {
	l, k := int64(len(in.InputIDs)), int64(len(in.MarkerPos))
	mask := make([]int64, l)
	for i := range mask {
		mask[i] = 1
	}
	markers := make([]bool, k)
	for i := range markers {
		markers[i] = true
	}
	qtype := []int64{in.QType}
	var vals [5]uintptr
	defer func() {
		for _, v := range vals {
			s.release(slotReleaseValue, v)
		}
	}()
	for i, t := range []struct {
		data  unsafe.Pointer
		bytes int64
		elem  uintptr
		shape []int64
	}{
		{unsafe.Pointer(&in.InputIDs[0]), l * 8, elemInt64, []int64{1, l}},
		{unsafe.Pointer(&mask[0]), l * 8, elemInt64, []int64{1, l}},
		{unsafe.Pointer(&in.MarkerPos[0]), k * 8, elemInt64, []int64{1, k}},
		{unsafe.Pointer(&markers[0]), k, elemBool, []int64{1, k}},
		{unsafe.Pointer(&qtype[0]), 8, elemInt64, []int64{1}},
	} {
		if err := s.call(slotCreateTensorWithDataAsOrtValue, s.info, uintptr(t.data), uintptr(t.bytes),
			uintptr(unsafe.Pointer(&t.shape[0])), uintptr(len(t.shape)), t.elem, uintptr(unsafe.Pointer(&vals[i]))); err != nil {
			return Output{}, err
		}
	}
	var outs [2]uintptr
	defer func() {
		for _, v := range outs {
			s.release(slotReleaseValue, v)
		}
	}()
	err := s.call(slotRun, s.sess, 0, uintptr(unsafe.Pointer(&inputNames[0])), uintptr(unsafe.Pointer(&vals[0])), uintptr(len(vals)),
		uintptr(unsafe.Pointer(&outputNames[0])), uintptr(len(outs)), uintptr(unsafe.Pointer(&outs[0])))
	runtime.KeepAlive(in)
	runtime.KeepAlive(mask)
	runtime.KeepAlive(markers)
	runtime.KeepAlive(qtype)
	if err != nil {
		return Output{}, err
	}
	var out Output
	logits, err := s.floats(outs[0], int(k))
	if err != nil {
		return Output{}, err
	}
	act, err := s.floats(outs[1], 2)
	if err != nil {
		return Output{}, err
	}
	out.Logits = logits
	copy(out.ActLogits[:], act)
	return out, nil
}

// floats copies n floats out of the tensor v.
func (s *session) floats(v uintptr, n int) ([]float32, error) {
	var p uintptr
	if err := s.call(slotGetTensorMutableData, v, uintptr(unsafe.Pointer(&p))); err != nil {
		return nil, err
	}
	return slices.Clone(unsafe.Slice((*float32)(cptr(p)), n)), nil
}

func (s *session) close() {
	s.release(slotReleaseMemoryInfo, s.info)
	s.release(slotReleaseSession, s.sess)
	s.release(slotReleaseSessionOptions, s.opts)
	s.release(slotReleaseEnv, s.env)
	*s = session{api: s.api}
}

func (s *session) fn(slot int) uintptr {
	return unsafe.Slice((*uintptr)(cptr(s.api)), slotReleaseSessionOptions+1)[slot]
}

// call calls a C API function that returns an OrtStatus*, NULL on success.
func (s *session) call(slot int, args ...uintptr) error {
	st, _, _ := purego.SyscallN(s.fn(slot), args...)
	if st == 0 {
		return nil
	}
	msg, _, _ := purego.SyscallN(s.fn(slotGetErrorMessage), st)
	err := fmt.Errorf("laya: ONNX Runtime: %s", gostring(msg))
	purego.SyscallN(s.fn(slotReleaseStatus), st)
	return err
}

func (s *session) release(slot int, v uintptr) {
	if v != 0 {
		purego.SyscallN(s.fn(slot), v)
	}
}

// cstring is s NUL-terminated, in Go memory.
func cstring(s string) *byte { return &append([]byte(s), 0)[0] }

func cstrings(ss ...string) []*byte {
	out := make([]*byte, len(ss))
	for i, s := range ss {
		out[i] = cstring(s)
	}
	return out
}

// gostring copies the NUL-terminated string at p.
func gostring(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(cptr(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(cptr(p)), n))
}

// cptr is the address p of C memory, which Go's collector never moves or
// frees, as a pointer.
func cptr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

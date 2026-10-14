// Package laya runs the Laya decision model locally, in a worker process of
// its own: ONNX Runtime keeps the memory of the longest input it ran until
// its process ends (released sessions and environments included), so the
// app's process never loads it. A Client starts the worker on demand and
// stops it when idle; Serve is the worker.
//
// The worker speaks JSON lines on stdin and stdout: first a hello line
// (ready, or an error and exit), then one response line per request line,
// in order. It exits when stdin ends.
package laya

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxTokens is the model's context.
const MaxTokens = 1024

// Question types of Input.QType.
const (
	QChoice = 0
	QScore  = 1
	QNoul   = 2
)

// Input is one question: its token sequence and the positions of its option
// markers in it.
type Input struct {
	InputIDs  []int64 `json:"input_ids"`
	MarkerPos []int64 `json:"marker_pos"`
	QType     int64   `json:"qtype"`
}

// Output is the model's raw answer: a logit per marker, and the action
// logits.
type Output struct {
	Logits    []float32  `json:"logits"`
	ActLogits [2]float32 `json:"act_logits"`
}

// Validate checks in against the graph's shapes.
func (in Input) Validate() error {
	l := int64(len(in.InputIDs))
	switch {
	case l == 0 || l > MaxTokens:
		return fmt.Errorf("laya: %d tokens, want 1 to %d", l, MaxTokens)
	case len(in.MarkerPos) == 0:
		return errors.New("laya: no markers")
	case in.QType < QChoice || in.QType > QNoul:
		return fmt.Errorf("laya: question type %d", in.QType)
	}
	for _, p := range in.MarkerPos {
		if p < 0 || p >= l {
			return fmt.Errorf("laya: marker at %d of %d tokens", p, l)
		}
	}
	return nil
}

// Config is the worker's model.
type Config struct {
	Lib     string // absolute path of the ONNX Runtime library, ORTVersion
	Model   string // the ONNX graph; its weights best in external data, which is mapped, not copied
	Threads int    // intra-op threads
}

// hello is the worker's first line.
type hello struct {
	Ready bool   `json:"ready,omitempty"`
	Error string `json:"error,omitempty"`
}

// response is the worker's line for a request.
type response struct {
	*Output
	Error string `json:"error,omitempty"`
}

// Serve is the worker: it loads the model, says hello on w, then answers
// each request read from r until r ends. A request it cannot answer gets an
// error line; only a broken stream or a failed load end it with an error.
func Serve(r io.Reader, w io.Writer, cfg Config) error {
	enc := json.NewEncoder(w)
	s, err := openSession(cfg.Lib, cfg.Model, cfg.Threads)
	if err != nil {
		return errors.Join(err, enc.Encode(hello{Error: err.Error()}))
	}
	defer s.close()
	if err := enc.Encode(hello{Ready: true}); err != nil {
		return err
	}
	dec := json.NewDecoder(bufio.NewReader(r))
	for {
		var in Input
		if err := dec.Decode(&in); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var resp response
		if err := in.Validate(); err != nil {
			resp.Error = err.Error()
		} else if out, err := s.run(in); err != nil {
			resp.Error = err.Error()
		} else {
			resp.Output = &out
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}

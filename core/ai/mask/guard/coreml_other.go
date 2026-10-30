//go:build !darwin

package guard

import "errors"

// CoreMLLibrary is the Mac's CoreML bridge; elsewhere there is none.
var CoreMLLibrary string

func loadML() error { return errors.New("no CoreML here") }

func mlCompile(pkg, out string) error { return loadML() }

type mlModel struct{ Device string }

func mlOpen(path string) (*mlModel, error) { return nil, loadML() }

func (m *mlModel) Run(x, mask []float32, s, h, k int) ([]float32, error) { return nil, loadML() }

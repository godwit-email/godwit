package guard

import (
	"errors"
	"testing"
)

// failingML is a CoreML program whose every read fails.
type failingML struct{ reads int }

func (f *failingML) logits(*net, []int) ([]float32, error) {
	f.reads++
	return nil, errors.New("the Neural Engine is busy")
}

// A CoreML read that fails leaves CoreML: the model says it reads on the
// CPU and why, and CoreML isn't asked again.
func TestCoreMLFailureIsRecorded(t *testing.T) {
	f := &failingML{}
	m := &Model{ml: f, mlDevice: "Neural Engine"}
	if dev, why := m.CoreML(); dev != "Neural Engine" || why != nil || m.Device() != "CoreML Neural Engine" {
		t.Fatalf("before: %q %v %q", dev, why, m.Device())
	}
	ids := make([]int, coremlMin)
	if _, ok := m.coreML(ids); ok {
		t.Fatal("a failed read gave logits")
	}
	dev, why := m.CoreML()
	if dev != "" || why == nil || why.Error() != "CoreML failed: the Neural Engine is busy" || m.Device() != "Go" {
		t.Fatalf("after: %q %v %q", dev, why, m.Device())
	}
	m.coreML(ids)
	if f.reads != 1 {
		t.Fatalf("CoreML read %d times", f.reads)
	}
}

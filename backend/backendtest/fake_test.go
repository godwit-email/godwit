package backendtest_test

import (
	"testing"

	"github.com/godwit-email/godwit/backend/backendtest"
)

// The contract runs from outside its package against Fake.
func TestContract(t *testing.T) {
	backendtest.Run(t, func(*testing.T) backendtest.Server { return backendtest.NewFake() })
}

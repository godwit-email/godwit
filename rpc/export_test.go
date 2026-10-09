package rpc

import "context"

// WithHooks returns o with test seams around stream registration; nil funcs are skipped.
func WithHooks(o ConnOptions, afterCall func(context.Context), afterRegister, beforeCancel func()) ConnOptions {
	o.hooks = testHooks{afterCall: afterCall, afterRegister: afterRegister, beforeCancel: beforeCancel}
	return o
}

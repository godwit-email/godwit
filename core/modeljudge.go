package core

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
)

type pluginJudgeRuntime struct {
	gate, prepareGate      chan struct{}
	mu                     sync.Mutex
	releaseMu              sync.Mutex
	loaded                 *loadedPluginModel
	states                 map[string]runtimeModelStatus
	leases                 map[uint64][]func()
	generation, nextWaiter uint64
	prepares               map[string]*modelPrepareTask
}

func newPluginJudgeRuntime() *pluginJudgeRuntime {
	return &pluginJudgeRuntime{gate: make(chan struct{}, 1), prepareGate: make(chan struct{}, 1)}
}

type pluginJudge struct {
	s    *Service
	name string
}
type pluginJudgeResult struct {
	answers []ai.Answer
	err     error
}
type nativeModelCallKey struct{}
type nativeModelCall struct {
	rp      *runningPlugin
	session plugins.NativeSession
}

func (j pluginJudge) Ask(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	if err := ai.ValidateQuestions(qs); err != nil {
		return nil, err
	}
	if err := plugins.CheckModelReentry(ctx, j.name); err != nil {
		return nil, modelCallError(err)
	}
	// Reject oversized input before handing data to a plugin or taking its slot.
	for _, q := range qs {
		b, err := json.Marshal(plugins.JudgeModelRequest{ModelID: j.name, Input: input, Questions: []ai.Question{q}})
		if err != nil {
			return nil, err
		}
		if len(b) > plugins.MaxResultBytes {
			return nil, plugins.ErrTooLarge
		}
	}
	snap, err := j.s.captureModelSnapshot(ctx, j.name)
	if err != nil {
		return nil, modelCallError(err)
	}
	rt := snap.rp.modelJudge
	select {
	case rt.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-rt.gate
		return nil, err
	}
	workerCtx := ctx
	if snap.rp.native != nil {
		workerCtx = context.WithoutCancel(ctx)
	}
	work, end, err := j.s.begin(workerCtx, 0)
	if err != nil {
		<-rt.gate
		return nil, err
	}
	done := make(chan pluginJudgeResult, 1)
	go func() {
		defer end()
		defer func() { <-rt.gate }()
		var result pluginJudgeResult
		run := func(work context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			st, err := j.s.loadSettings(work)
			if err != nil {
				return err
			}
			bound, model, err := j.s.pluginModel(work, st.JudgeModel)
			if err != nil {
				return err
			}
			if bound.name != j.name || bound.rp != snap.rp || model.Transport != "plugin" {
				return modelUnavailable()
			}
			if err := j.s.ensurePluginModel(work, bound, model); err != nil {
				return err
			}
			for _, q := range qs {
				if err := ctx.Err(); err != nil {
					return err
				}
				var got plugins.JudgeModelResult
				err := j.s.callPluginModel(work, bound, "models.judge", plugins.JudgeModelRequest{ModelID: model.ID, Input: input, Questions: []ai.Question{q}}, &got)
				if err != nil {
					return err
				}
				if !j.s.modelSnapshotCurrent(work, bound) {
					return modelUnavailable()
				}
				answers, err := ai.ValidateAnswers([]ai.Question{q}, got.Answers)
				if err != nil {
					_ = j.s.pluginFailed(context.WithoutCancel(work), bound.name, bound.rp.gen)
					return ai.ErrJudgePlugin
				}
				if err = j.s.pluginSucceeded(work, bound.name, bound.rp.gen); err != nil {
					return err
				}
				result.answers = append(result.answers, answers...)
			}
			return nil
		}
		if snap.rp.native != nil {
			result.err = snap.rp.native.WithProcess(work, func(session plugins.NativeSession) error {
				return run(context.WithValue(work, nativeModelCallKey{}, nativeModelCall{rp: snap.rp, session: session}))
			})
		} else {
			result.err = run(work)
		}
		if result.err != nil {
			result.answers = nil
			result.err = modelCallError(result.err)
		}
		done <- result
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		return result.answers, result.err
	}
}

func modelCallError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var business *plugins.NativeError
	if errors.As(err, &business) {
		err = &ModelError{Code: business.Code, Message: business.Message, Retryable: business.Retryable}
	}
	var me *ModelError
	if errors.As(err, &me) {
		switch me.Code {
		case "model_unavailable", "model_not_ready", "insufficient_memory", "unsupported_platform", "update_required", "verification_failed":
			return errors.Join(ai.ErrJudgeUnavailable, err)
		}
		return err
	}
	if errors.Is(err, plugins.ErrTooLarge) || errors.Is(err, ai.ErrJudgeQuestion) {
		return err
	}
	return errors.Join(ai.ErrJudgePlugin, err)
}

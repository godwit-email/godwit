package core

// Judge models a plugin answers itself (transport "plugin"): one question per
// call and one call at a time per plugin; a native plugin's model with
// assets has its files verified and is loaded once per process first.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/plugins"
)

// errJudgeInput: the plugin judge cannot take this input (invalid_request),
// as a service answers 400: about this message alone.
var errJudgeInput = errors.New("the judge model cannot take this input")

// unavailableCodes are a plugin's error codes that mean its model cannot answer now.
var unavailableCodes = map[string]bool{codeModelNotReady: true, codeModelUnavailable: true, codeInsufficientMemory: true,
	codeUnsupportedPlatform: true, codeUpdateRequired: true, codeVerificationFailed: true}

type pluginModelJudge struct {
	s     *Service
	name  string
	rp    *runningPlugin
	model plugins.ProviderModel
	cfg   any // the settings snapshot it was resolved with: other settings may list other models
}

func (j pluginModelJudge) Ask(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	if err := ai.ValidateQuestions(qs); err != nil {
		return nil, err
	}
	out := make([]ai.Answer, 0, len(qs))
	for _, q := range qs {
		a, err := j.ask(ctx, input, q)
		if err != nil {
			return nil, err
		}
		out = append(out, a...)
	}
	return out, nil
}

// ask answers one question once the plugin's judge slot is free. A caller
// may give up while it waits or after dispatch, but a dispatched call runs
// to its end (the plugin's own timeout) and keeps the slot until then.
func (j pluginModelJudge) ask(ctx context.Context, input string, q ai.Question) ([]ai.Answer, error) {
	select {
	case j.rp.judging <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	type result struct {
		answers []ai.Answer
		err     error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-j.rp.judging }()
		a, err := j.dispatch(context.WithoutCancel(ctx), input, q)
		done <- result{a, err}
	}()
	select {
	case r := <-done:
		return r.answers, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// dispatch loads the model if needed and asks the plugin one question.
func (j pluginModelJudge) dispatch(ctx context.Context, input string, q ai.Question) ([]ai.Answer, error) {
	j.s.pluginsMu.RLock()
	stale := j.s.running[j.name] != j.rp || j.rp.cfgKey() != j.cfg // stopped, replaced or reconfigured
	j.s.pluginsMu.RUnlock()
	if stale {
		return nil, fmt.Errorf("%w: plugin/%s/%s", ErrModelUnavailable, j.name, j.model.ID)
	}
	if j.rp.native == nil {
		if err := j.s.markHoldsMail(ctx, j.name); err != nil { // the input is mail
			return nil, err
		}
	}
	if err := j.load(ctx); err != nil {
		return nil, err
	}
	qs := []ai.Question{q}
	m, req, err := j.masked(plugins.JudgeModelRequest{ModelID: j.model.ID, Input: input, Questions: qs})
	if err != nil {
		return nil, err // not sent: no fault of the plugin's
	}
	var answers []ai.Answer
	err = j.s.callModelPlugin(ctx, j.name, j.rp, func(ctx context.Context) error {
		var res plugins.JudgeModelResult
		var err error
		if j.rp.native != nil {
			v, cerr := j.rp.native.Call(ctx, "models.judge", req)
			err = decodeAnswer(v, judgeError(cerr), &res, "models.judge")
		} else {
			v, cerr := j.rp.p.JudgeModel(ctx, j.s.modelInvocation(j.rp, j.cfg), req)
			err = decodeAnswer(v, cerr, &res, "judge")
		}
		if err == nil {
			if answers, err = ai.ValidateAnswers(qs, ai.RestoreAnswers(m, res.Answers)); err != nil {
				err = fmt.Errorf("%w: %s", errPluginAnswer, err) // not ErrJudgeAnswer: the plugin is at fault, not this input
			}
		}
		return err
	})
	return answers, judgeFailure(ctx, err)
}

// masked is req as the plugin is asked it while masking is on: its input,
// instructions and options with placeholders, as an HTTP judge's body is
// (docs/adr/0025-mask-at-cloud-egress.md), and the masker that restores
// the answers.
func (j pluginModelJudge) masked(req plugins.JudgeModelRequest) (*mask.Masker, plugins.JudgeModelRequest, error) {
	m, err := j.s.masker()
	if err != nil || !m.On() {
		return m, req, err
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, req, err
	}
	body, err := m.Body(string(b))
	if err != nil {
		return nil, req, err
	}
	var out plugins.JudgeModelRequest
	return m, out, json.Unmarshal([]byte(body), &out)
}

// load verifies the files of a model with assets and has the plugin's
// process load it, once per process (setUpNative forgets what it loaded).
func (j pluginModelJudge) load(ctx context.Context) error {
	if j.rp.native == nil || len(j.model.Assets) == 0 {
		return nil
	}
	// A process that exited (idle, crash) is started first: its OnStart
	// forgets what the old one loaded before the cache is read.
	if err := j.rp.native.Start(ctx); err != nil {
		return judgeFailure(ctx, err)
	}
	proc := j.rp.procs.Load()                                                                                                            // a load the process after this one never saw does not count
	if v, ok := j.rp.loaded.Load(j.model.ID); ok && v.(loadedModel).proc == proc && maps.Equal(v.(loadedModel).assets, j.model.Assets) { // new settings may name other files
		return nil
	}
	dirs, lease, err := j.s.modelAssets().Verify(ctx, j.name, j.rp.row.assetsDigest, j.model.Assets)
	if err != nil {
		var me *ModelError
		if errors.As(err, &me) && unavailableCodes[me.Code] {
			return fmt.Errorf("%w: %w", ai.ErrJudgeUnavailable, err)
		}
		return err
	}
	var gen uint64 // the process that loaded it; 0 when that is not known
	err = j.s.callModelPlugin(ctx, j.name, j.rp, func(ctx context.Context) error {
		if err := j.rp.native.Start(ctx); err != nil { // verifying may have outlasted the idle timeout
			return err
		}
		gen = j.rp.procs.Load()
		_, err := j.rp.native.Call(ctx, "models.load", map[string]any{"modelId": j.model.ID, "assets": dirs})
		if j.rp.procs.Load() != gen || j.rp.exited.Load() == gen { // it ended, and maybe another started
			gen = 0
		}
		if ne := (*plugins.NativeError)(nil); errors.As(err, &ne) { // even invalid_request: no input is involved
			return fmt.Errorf("%w: %w", ai.ErrJudgeUnavailable, err)
		}
		return err
	})
	if err != nil {
		lease()
		return judgeFailure(ctx, err)
	}
	// Under pluginsMu, as stopPlugin's dropLoaded: a plugin stopped meanwhile never gets the lease.
	j.s.pluginsMu.RLock()
	defer j.s.pluginsMu.RUnlock()
	if j.s.running[j.name] != j.rp {
		lease()
		return plugins.ErrClosed
	}
	if gen == 0 { // nothing to remember: the next question loads it again
		lease()
		return nil
	}
	if old, had := j.rp.loaded.Swap(j.model.ID, loadedModel{j.model.Assets, lease, gen}); had { // calls are one at a time: no other load raced this one
		old.(loadedModel).lease()
	}
	if j.rp.exited.Load() == gen { // it ended before the entry went in (OnExit ran first): it holds nothing now
		if m, ok := j.rp.loaded.LoadAndDelete(j.model.ID); ok {
			m.(loadedModel).lease()
		}
	}
	// The plugin may have unloaded the others for this one: they load again when used.
	j.rp.loaded.Range(func(id, _ any) bool {
		if id != j.model.ID {
			if m, ok := j.rp.loaded.LoadAndDelete(id); ok {
				m.(loadedModel).lease()
			}
		}
		return true
	})
	return nil
}

// judgeError maps a native plugin's error envelope: invalid_request is about
// this input, any other code means the model cannot answer now.
func judgeError(err error) error {
	var ne *plugins.NativeError
	switch {
	case !errors.As(err, &ne):
		return err
	case ne.Code == "invalid_request":
		return fmt.Errorf("%w: %w", errJudgeInput, err)
	}
	return fmt.Errorf("%w: %w", ai.ErrJudgeUnavailable, err)
}

// judgeFailure marks what callModelPlugin returned that is neither the
// caller's cancellation nor an answer of the plugin's as ErrJudgePlugin.
func judgeFailure(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil || errors.Is(err, plugins.ErrClosed) ||
		errors.Is(err, ai.ErrJudgeUnavailable) || errors.Is(err, errJudgeInput) {
		return err
	}
	return fmt.Errorf("%w: %w", ai.ErrJudgePlugin, err)
}

// loadedModel is a model a process loaded: the assets it was loaded with,
// their files' lease, and the process (runningPlugin.procs) it was loaded in.
type loadedModel struct {
	assets map[string]string
	lease  func()
	proc   uint64
}

// dropLoaded forgets the models rp's process loaded, releasing their files.
func (rp *runningPlugin) dropLoaded() {
	rp.loaded.Range(func(id, _ any) bool {
		if m, ok := rp.loaded.LoadAndDelete(id); ok { // the value removed, which a concurrent Swap may have replaced
			m.(loadedModel).lease()
		}
		return true
	})
}

// modelAssets is the model files' manager (modelseam.go).
func (s *Service) modelAssets() modelAssets {
	if s.assets == nil {
		return noModelAssets{}
	}
	return s.assets
}

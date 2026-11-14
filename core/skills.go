package core

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/skills"
)

var (
	ErrSkillDisabled = errors.New("skill is disabled")
	ErrSkillNotFound = errors.New("skill not found")
)

type SkillInfo struct {
	Name, Description string
	DisplayName, Icon string
	On                []string
	Output            string
	Model             string // godwit-model; "" is the default model
	Tier              string // godwit-model-tier: "small" or ""
	Builtin, Enabled  bool
	UI                bool // has godwit-action-*
	Error             string
	ManagedBy         string // the agent gateway whose library it comes from (ADR 0041); "" otherwise
}

func (s SkillInfo) Label() string { return skills.Label(s.Name, s.DisplayName) }

// loadSkills merges the built-ins with DataDir/skills; a user skill replaces a
// built-in of the same name, even a broken one (it is reported, not hidden).
// Skills an agent gateway put in ExternalDir/skills come last and replace
// nothing (ADR 0041).
func (s *Service) loadSkills() (ok map[string]skills.Skill, failed map[string]error, err error) {
	ok = map[string]skills.Skill{}
	for _, b := range skills.Builtins() {
		ok[b.Name] = b
	}
	good, failed, err := skills.LoadDir(filepath.Join(s.dataDir, "skills"))
	if err != nil {
		return nil, nil, err
	}
	for n := range failed {
		delete(ok, n)
	}
	for _, u := range good {
		ok[u.Name] = u
	}
	if s.opts.ExternalDir == "" {
		return ok, failed, nil
	}
	ext, extFailed, err := skills.LoadLinkedDir(filepath.Join(s.opts.ExternalDir, externalSkills))
	if err != nil { // the gateway's folder must not take the user's skills down with it
		slog.Warn("external skills unreadable", "err", err)
		return ok, failed, nil
	}
	for _, e := range ext {
		if _, taken := ok[e.Name]; !taken && failed[e.Name] == nil {
			ok[e.Name] = e
		}
	}
	for n, e := range extFailed {
		if _, taken := ok[n]; !taken && failed[n] == nil {
			failed[n] = e
		}
	}
	return ok, failed, nil
}

func (s *Service) disabledSkills(ctx context.Context) ([]string, error) {
	return s.readList(ctx, skillsOff)
}

// ListSkills lists built-in and user skills, including user skills that failed to load.
func (s *Service) ListSkills(ctx context.Context) ([]SkillInfo, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	off, err := s.disabledSkills(ctx)
	if err != nil {
		return nil, err
	}
	ok, failed, err := s.loadSkills()
	if err != nil {
		return nil, err
	}
	var out []SkillInfo
	for n, sk := range ok {
		out = append(out, SkillInfo{Name: n, DisplayName: sk.DisplayName, Icon: sk.Icon, Description: sk.Description, On: sk.On, Output: sk.Output, Model: sk.Model, Tier: sk.Tier,
			Builtin: sk.Builtin, Enabled: !slices.Contains(off, n), UI: len(sk.On) > 0, ManagedBy: s.managedBy(sk.External)})
	}
	for n, e := range failed {
		out = append(out, SkillInfo{Name: n, Icon: skills.IconOrDefault(""), Enabled: !slices.Contains(off, n), Error: e.Error()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SetSkillEnabled turns a skill on or off.
func (s *Service) SetSkillEnabled(ctx context.Context, name string, enabled bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.skillsMu.Lock() // serialize the read-modify-write of skills.disabled
	defer s.skillsMu.Unlock()
	ok, failed, err := s.loadSkills()
	if err != nil {
		return err
	}
	_, known := ok[name]
	if _, bad := failed[name]; !known && !bad {
		return ErrSkillNotFound
	}
	if err := s.setSkillEnabled(ctx, name, enabled); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.SkillsChanged})
	return nil
}

// 调用方持有 skillsMu；新建与手动启停共用这份禁用状态。
func (s *Service) setSkillEnabled(ctx context.Context, name string, enabled bool) error {
	off, err := s.disabledSkills(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(off, name) == !enabled {
		return nil
	}
	off = slices.DeleteFunc(off, func(n string) bool { return n == name })
	if !enabled {
		off = append(off, name)
	}
	return s.writeList(ctx, skillsOff, off)
}

// skill returns an enabled, loadable skill.
func (s *Service) skill(ctx context.Context, name string) (skills.Skill, error) {
	ok, failed, err := s.loadSkills()
	if err != nil {
		return skills.Skill{}, err
	}
	sk, found := ok[name]
	if !found {
		if e, bad := failed[name]; bad {
			return skills.Skill{}, e
		}
		return skills.Skill{}, ErrSkillNotFound
	}
	off, err := s.disabledSkills(ctx)
	if err != nil {
		return skills.Skill{}, err
	}
	if slices.Contains(off, name) {
		return skills.Skill{}, ErrSkillDisabled
	}
	return sk, nil
}

package core

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sort"

	"github.com/godwit-email/godwit/core/skills"
)

var (
	ErrSkillDisabled = errors.New("skill is disabled")
	ErrSkillNotFound = errors.New("skill not found")
)

type SkillInfo struct {
	Name, Description string
	On                []string
	Output            string
	Model             string // godwit-model; "" is the default model
	Builtin, Enabled  bool
	UI                bool // has godwit-action-*
	Error             string
}

// loadSkills merges the built-ins with DataDir/skills; a user skill replaces a
// built-in of the same name, even a broken one (it is reported, not hidden).
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
	return ok, failed, nil
}

func (s *Service) disabledSkills(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, "SELECT value FROM settings WHERE key = 'skills.disabled'")
	if err != nil || len(rows.Values) == 0 {
		return nil, err
	}
	var names []string
	return names, json.Unmarshal([]byte(rows.Values[0][0].(string)), &names)
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
		out = append(out, SkillInfo{Name: n, Description: sk.Description, On: sk.On, Output: sk.Output, Model: sk.Model,
			Builtin: sk.Builtin, Enabled: !slices.Contains(off, n), UI: len(sk.On) > 0})
	}
	for n, e := range failed {
		out = append(out, SkillInfo{Name: n, Enabled: !slices.Contains(off, n), Error: e.Error()})
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
	off, err := s.disabledSkills(ctx)
	if err != nil {
		return err
	}
	off = slices.DeleteFunc(off, func(n string) bool { return n == name })
	if !enabled {
		off = append(off, name)
	}
	b, _ := json.Marshal(off)
	_, err = s.db.Execute(ctx, `INSERT INTO settings(key, value) VALUES ('skills.disabled', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b))
	return err
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

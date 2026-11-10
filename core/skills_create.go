package core

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/skills"
)

type createSkillArgs struct {
	Name         string   `json:"name"`
	DisplayName  string   `json:"display_name"`
	Icon         string   `json:"icon"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	On           []string `json:"on,omitempty"`
	Output       string   `json:"output,omitempty"`
}

func (s *Service) registerCreateSkillTool() {
	s.mustRegister(toolDesc{Spec: ai.ToolSpec{
		Name: "create_skill",
		Description: "Create a new reusable, prompt-only user skill when the user asks to save instructions as a skill. " +
			"The user reviews the stable name, optional display_name and built-in icon, description, instructions and UI binding and approves before SKILL.md is saved locally. " +
			"Never replaces an existing or built-in skill. Does not run the skill, execute scripts, change model settings or send mail. " +
			"Omit on and output for a Wit skill available as /name. If the user wants to analyze mail in Wit or continue with follow-up questions, use on: [message], output: wit. " +
			"For a result card inside the reader only, use on: [message], output: panel. wit requires exactly on: [message]. " +
			"UI bindings require both on and output. draft requires replace-draft; message and thread cannot use replace-draft. " +
			"Instructions are reusable prompts, not mail contents or credentials.",
		Schema: object(map[string]any{
			"name":         map[string]any{"type": "string", "pattern": "^[a-z0-9]+(-[a-z0-9]+)*$", "maxLength": 64},
			"display_name": map[string]any{"type": "string", "maxLength": 80, "description": "Optional single-line display name; name remains the stable /name ID."},
			"icon":         map[string]any{"type": "string", "enum": append([]string{""}, skills.IconNames()...), "description": "Optional built-in icon key; defaults to sparkles."},
			"description":  stringProp,
			"instructions": stringProp,
			"on": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "uniqueItems": true,
				"items": map[string]any{"type": "string", "enum": []string{"message", "thread", "selection", "draft"}}},
			"output": map[string]any{"type": "string", "enum": []string{"panel", "replace-body", "replace-draft", "wit"}},
		}, "name", "description", "instructions"),
	}, Write: true, Mode: modeExternal, Prepare: s.prepareCreateSkill, Run: s.createSkill})
}

func skillDefinition(args json.RawMessage) (createSkillArgs, []byte, error) {
	var in createSkillArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return in, nil, err
	}
	in.Description, in.Instructions = strings.TrimSpace(in.Description), strings.TrimSpace(in.Instructions)
	if !skills.ValidName(in.Name) || len(in.Name) > 64 {
		return in, nil, errors.New("skill name must use lowercase letters, digits and single hyphens, at most 64 characters")
	}
	if in.DisplayName != "" && (strings.TrimSpace(in.DisplayName) == "" || utf8.RuneCountInString(in.DisplayName) > 80 || strings.ContainsFunc(in.DisplayName, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	})) {
		return in, nil, errors.New("skill display_name must be a non-blank single line of at most 80 characters, without control characters")
	}
	if in.Icon != "" && !skills.ValidIcon(in.Icon) {
		return in, nil, fmt.Errorf("skill icon %q must be a built-in icon key", in.Icon)
	}
	in.Icon = skills.IconOrDefault(in.Icon)
	if in.Instructions == "" {
		return in, nil, errors.New("skill instructions are required")
	}
	var fm struct {
		Name        string            `yaml:"name"`
		Description string            `yaml:"description"`
		Metadata    map[string]string `yaml:"metadata,omitempty"`
	}
	fm.Name, fm.Description = in.Name, in.Description
	fm.Metadata = map[string]string{"godwit-icon": in.Icon}
	if in.DisplayName != "" {
		fm.Metadata["godwit-display-name"] = in.DisplayName
	}
	if len(in.On) > 0 || in.Output != "" {
		fm.Metadata["godwit-action-on"] = strings.Join(in.On, ",")
		fm.Metadata["godwit-action-output"] = in.Output
	}
	header, err := yaml.Marshal(fm)
	if err != nil {
		return in, nil, err
	}
	raw := []byte("---\n" + string(header) + "---\n\n" + in.Instructions + "\n")
	if len(raw) > skills.MaxFileBytes {
		return in, nil, fmt.Errorf("skill must not exceed %d bytes", skills.MaxFileBytes)
	}
	sk, err := skills.ParseContent(in.Name, raw)
	if err != nil {
		return in, nil, err
	}
	if !slices.Equal(in.On, sk.On) {
		return in, nil, errors.New("each skill target must be one of message, thread, selection or draft")
	}
	for i, target := range in.On {
		if slices.Index(in.On, target) != i {
			return in, nil, errors.New("skill targets must be distinct")
		}
		if target == "draft" && in.Output != "replace-draft" ||
			(target == "message" || target == "thread") && in.Output == "replace-draft" {
			return in, nil, fmt.Errorf("skill output %q cannot run on %q", in.Output, target)
		}
	}
	return in, raw, nil
}

// 创建入口不继承手工安装时覆盖内置 Skill 的能力。
func (s *Service) newSkillName(ctx context.Context, name string) error {
	for _, sk := range skills.Builtins() {
		if sk.Name == name {
			return fmt.Errorf("skill %q is built in; choose a new name", name)
		}
	}
	off, err := s.disabledSkills(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(off, name) {
		return fmt.Errorf("skill %q is disabled; choose a new name", name)
	}
	if _, err := os.Lstat(filepath.Join(s.dataDir, "skills", name)); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return fmt.Errorf("skill %q already exists; choose a new name", name)
	}
	return nil
}

func (s *Service) prepareCreateSkill(ctx context.Context, _ Conversation, args json.RawMessage) (prepared, error) {
	in, _, err := skillDefinition(args)
	if err != nil {
		return prepared{}, err
	}
	if err := s.newSkillName(ctx, in.Name); err != nil {
		return prepared{}, err
	}
	args, err = json.Marshal(in)
	return prepared{Args: args, Summary: in.Name + " — " + in.Description, TargetKind: "skill"}, err
}

func (s *Service) createSkill(ctx context.Context, a approvalRow) (any, error) {
	in, raw, err := skillDefinition(a.Args)
	if err != nil {
		return nil, err
	}
	s.skillsMu.Lock()
	defer s.skillsMu.Unlock()
	if err := s.newSkillName(ctx, in.Name); err != nil {
		return nil, err
	}
	if err := s.saveNewSkill(ctx, in.Name, raw); err != nil {
		return nil, err
	}
	s.bus.Publish(events.Event{Type: events.SkillsChanged})
	return map[string]any{"name": in.Name, "created": true, "on": in.On, "output": in.Output}, nil
}

// 文件写入不能加入审批的数据库事务；external 路径负责崩溃后的不确定结果，绝不重试。
func (s *Service) saveNewSkill(ctx context.Context, name string, raw []byte) error {
	root, err := os.OpenRoot(s.dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir("skills", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := root.Lstat("skills")
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return errors.New("the skills directory must not be a symlink")
	}
	dir, err := root.OpenRoot("skills")
	if err != nil {
		return err
	}
	defer dir.Close()

	// 临时文件不是目录，不会被 Skill 加载器发现；完成后才发布 SKILL.md。
	tmp := ".tmp-" + rand.Text()
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer dir.Remove(tmp)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := dir.Mkdir(name, 0o700); err != nil {
		return err
	}
	// Link 不替换已有目标，即使审批后有另一个创建者抢先写入也不会覆盖。
	if err := dir.Link(tmp, name+"/SKILL.md"); err != nil {
		dir.Remove(name) // 只移除空目录，保留别人新写入的内容。
		return err
	}
	for _, p := range []string{"skills/" + name, "skills", "."} {
		if err := syncSkillDir(root, p); err != nil {
			return fmt.Errorf("skill %q was written but its directory could not be synced: %w", name, err)
		}
	}
	return nil
}

func syncSkillDir(root *os.Root, name string) error {
	if runtime.GOOS == "windows" { // NTFS 记录目录变更，但 Windows 不允许 fsync 目录。
		return nil
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
)

// An agent gateway such as magpie connects Godwit by writing these files into
// Options.ExternalDir, the way it writes any agent's own config; Godwit mirrors
// them as managed model services, MCP servers, skills and Wit instructions
// (ADR 0041). The gateway restores the files when the user disconnects, and
// what they no longer list goes.
const (
	externalModels       = "models.json"   // {"<name>": {by, baseURL, apiKey, kind, models, model}}
	externalMCP          = "mcp.json"      // {"mcpServers": {...}}, Claude's format
	externalSkills       = "skills"        // a folder per skill, possibly linked
	externalInstructions = "AGENTS.md"     // the user's standing instructions
	maxExternalFile      = 1 << 20         // a larger file is ignored
	maxExternalModels    = 1000            // per service
	maxInstructions      = 16 << 10        // bytes of AGENTS.md Wit reads
	externalDefaultBy    = "external"      // ManagedBy when models.json names no gateway
	externalSyncEvery    = 3 * time.Second // the gateway writes rarely; a stat is cheap
	externalByKey        = "external.by"   // settings row: the gateway models.json last named
)

var externalKinds = []string{ai.KindOpenAI, ai.KindResponses, ai.KindAnthropic}

type externalService struct {
	By      string          `json:"by"`
	BaseURL string          `json:"baseURL"`
	APIKey  string          `json:"apiKey"`
	Kind    string          `json:"kind"`
	Models  []externalModel `json:"models"`
	Model   string          `json:"model"`
	bad     error           // the entry did not parse: skipped, and what it had is kept
}

// parseExternalServices reads models.json entry by entry, so one entry it
// cannot read keeps its service as it was and the others still apply.
func parseExternalServices(data []byte) (map[string]externalService, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]externalService, len(raw))
	for name, r := range raw {
		var e externalService
		if err := json.Unmarshal(r, &e); err != nil {
			e = externalService{bad: err}
		}
		out[name] = e
	}
	return out, nil
}

// externalModel is a model as the gateway describes it (magpie's connect
// input); Godwit reads what it uses and leaves the rest (efforts, pricing…).
type externalModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Context int    `json:"context"` // tokens
}

func (e externalService) ids() []string {
	ids := make([]string, len(e.Models))
	for i, m := range e.Models {
		ids[i] = m.ID
	}
	return ids
}

type externalMCPServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// startExternalSync mirrors ExternalDir now and whenever a file in it changes.
// It creates the folder first: a gateway tells Godwit is installed by it.
func (s *Service) startExternalSync() {
	if s.opts.ExternalDir == "" {
		return
	}
	if err := os.MkdirAll(s.opts.ExternalDir, 0o700); err != nil {
		slog.Warn("external folder", "err", err)
	}
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	go func() {
		defer end()
		t := time.NewTicker(externalSyncEvery)
		defer t.Stop()
		for {
			s.syncExternal(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// syncExternal applies models.json and mcp.json if either changed since the
// last call. A file it cannot read or parse changes nothing: the gateway may
// be halfway through writing it.
func (s *Service) syncExternal(ctx context.Context) {
	s.extMu.Lock()
	defer s.extMu.Unlock()
	if s.extSeen == nil {
		s.extSeen = map[string]string{}
	}
	if s.extDirty.Swap(false) { // the files are applied again; the skills stamp is no config and stays
		delete(s.extSeen, externalModels)
		delete(s.extSeen, externalMCP)
	}
	models, mStamp, mChanged, mErr := s.readExternal(externalModels)
	var services map[string]externalService
	if mErr == nil && models != nil {
		services, mErr = parseExternalServices(models)
	}
	if s.extBy.Load() == nil { // what an earlier run saw: a disconnected entry names no gateway
		if l, err := s.readList(ctx, externalByKey); err == nil && len(l) == 1 && validManager(l[0]) {
			s.extBy.Store(&l[0])
		}
	}
	if mChanged && mErr == nil {
		for _, n := range slices.Sorted(maps.Keys(services)) {
			if b := services[n].By; validManager(b) {
				if cur := s.extBy.Load(); cur == nil || *cur != b {
					s.extBy.Store(&b)
					if err := s.writeList(ctx, externalByKey, []string{b}); err != nil {
						slog.Warn("external gateway name not saved", "err", err)
					}
				}
				break
			}
		}
	}
	if mChanged {
		if mErr != nil {
			slog.Warn("external models unreadable", "err", mErr)
		} else if err := s.syncExternalModels(ctx, services); err != nil {
			slog.Warn("external models, tried again next time", "err", err)
		} else {
			s.extSeen[externalModels] = mStamp
		}
	}
	// Skills are read when asked for; a change only tells the interfaces to ask again.
	if st := skillsStamp(filepath.Join(s.opts.ExternalDir, externalSkills)); st != s.extSeen[externalSkills] {
		if _, seen := s.extSeen[externalSkills]; seen || st != "" {
			s.bus.Publish(events.Event{Type: events.SkillsChanged})
		}
		s.extSeen[externalSkills] = st
	}
	mcp, cStamp, cChanged, cErr := s.readExternal(externalMCP)
	if !cChanged {
		return
	}
	var servers struct {
		MCPServers map[string]externalMCPServer `json:"mcpServers"`
	}
	if cErr == nil && mcp != nil {
		cErr = json.Unmarshal(mcp, &servers)
	}
	if cErr != nil {
		slog.Warn("external MCP servers unreadable", "err", cErr)
		return
	}
	if err := s.syncExternalMCP(ctx, servers.MCPServers, s.managedBy(true)); err != nil {
		slog.Warn("external MCP servers, tried again next time", "err", err)
		return
	}
	s.extSeen[externalMCP] = cStamp
}

// stamp is a file's size and modification time, "" when it is missing. Any
// other failure is an error: a file Godwit cannot see is not a file gone.
func (s *Service) stamp(name string) (string, error) {
	st, err := os.Stat(filepath.Join(s.opts.ExternalDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprint(st.Size(), st.ModTime().UnixNano()), nil
}

// readExternal reads file name if its stamp changed: nil data for a missing
// file, which lists nothing. st is the stamp taken before reading: the one to
// record once data is applied, so a write meanwhile is seen next time.
func (s *Service) readExternal(name string) (data []byte, st string, changed bool, err error) {
	if st, err = s.stamp(name); err != nil {
		return nil, "", true, err
	}
	if seen, ok := s.extSeen[name]; ok && seen == st {
		return nil, st, false, nil
	}
	if st == "" {
		return nil, st, true, nil
	}
	p := filepath.Join(s.opts.ExternalDir, name)
	info, err := os.Stat(p)
	if err == nil && info.Size() > maxExternalFile {
		err = fmt.Errorf("%s is larger than %d bytes", name, maxExternalFile)
	}
	if err == nil {
		data, err = os.ReadFile(p)
	}
	return data, st, true, err
}

// skillsStamp is each skill folder's name and its SKILL.md's size and
// modification time, a link followed; "" for no skills.
func skillsStamp(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range ents {
		st, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s %d %d\n", e.Name(), st.Size(), st.ModTime().UnixNano())
	}
	return b.String()
}

func validManager(by string) bool {
	return by != "" && len(by) <= 32 && !strings.ContainsFunc(by, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) && r != ' ' })
}

// managedBy is the ManagedBy of something loaded from ExternalDir.
func (s *Service) managedBy(external bool) string {
	if !external {
		return ""
	}
	if by := s.extBy.Load(); by != nil {
		return *by
	}
	return externalDefaultBy
}

// entryHash identifies what an entry configures, its secrets included, so an
// unchanged file changes nothing (an MCP server keeps its grant).
func entryHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type managedRow struct {
	id             int64
	by, hash, slug string
	fileModel      string
}

// checkExternalService validates an entry: the gateway is a program on this
// computer, so its address must be one; it is the only destination the
// entry can add.
func checkExternalService(name string, e *externalService) error {
	if slugify(name) != name || len(name) > 64 {
		return errors.New("the name must be lowercase letters, digits and hyphens")
	}
	if !validManager(e.By) {
		e.By = externalDefaultBy
	}
	base, err := ai.NormalizeBaseURL(e.BaseURL)
	if err != nil {
		return err
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	if h := u.Hostname(); !strings.EqualFold(h, "localhost") && (net.ParseIP(h) == nil || !net.ParseIP(h).IsLoopback()) {
		return errors.New("the address must be on this computer")
	}
	e.BaseURL = base
	switch {
	case !slices.Contains(externalKinds, e.Kind):
		return fmt.Errorf("unknown kind %q", e.Kind)
	case e.APIKey == "":
		return errors.New("no API key")
	case len(e.Models) == 0 || len(e.Models) > maxExternalModels:
		return fmt.Errorf("between 1 and %d models", maxExternalModels)
	case slices.ContainsFunc(e.Models, func(m externalModel) bool {
		return m.ID == "" || len(m.ID) > 200 || strings.ContainsFunc(m.ID, unicode.IsControl)
	}):
		return errors.New("a model id is empty, too long or has control characters")
	}
	seen := map[string]bool{}
	e.Models = slices.DeleteFunc(e.Models, func(m externalModel) bool { // a model listed twice counts once
		dup := seen[m.ID]
		seen[m.ID] = true
		return dup
	})
	if e.Model != "" && !seen[e.Model] {
		return fmt.Errorf("model %q is not among its models", e.Model)
	}
	for i := range e.Models { // only what is said of a model is lost, never the service
		if m := &e.Models[i]; len(m.Name) > 200 || strings.ContainsFunc(m.Name, unicode.IsControl) {
			m.Name = ""
		}
		if m := &e.Models[i]; m.Context < 1000 || m.Context > 100_000_000 {
			m.Context = 0
		}
	}
	return nil
}

// ownModels is what e says of its models, nil when nothing.
func (e externalService) ownModels() map[string]OwnModel {
	var out map[string]OwnModel
	for _, em := range e.Models {
		m := OwnModel{Name: em.Name, Context: em.Context}
		if m.Name == em.ID {
			m.Name = "" // no name of its own
		}
		if m != (OwnModel{}) {
			if out == nil {
				out = map[string]OwnModel{}
			}
			out[em.ID] = m
		}
	}
	return out
}

// loadExtModels keeps the managed services' model info in memory for
// historyBudget, which has no context to read the database with.
func (s *Service) loadExtModels(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT base_url, model_info FROM model_services WHERE managed_by != ''")
	if err != nil {
		return err
	}
	all := map[string]OwnModel{}
	for _, r := range rows.Values {
		var own map[string]OwnModel
		if err := json.Unmarshal([]byte(store.Str(r[1])), &own); err != nil {
			return err
		}
		for id, m := range own {
			all[store.Str(r[0])+"\n"+id] = m
		}
	}
	s.extModels.Store(&all)
	return nil
}

func (s *Service) managedServices(ctx context.Context) (map[string]managedRow, error) {
	rows, err := s.db.Query(ctx, "SELECT id, name, managed_by, managed_hash, slug, file_model FROM model_services")
	if err != nil {
		return nil, err
	}
	out := map[string]managedRow{}
	for _, r := range rows.Values {
		out[store.Str(r[1])] = managedRow{id: store.Int(r[0]), by: store.Str(r[2]), hash: store.Str(r[3]), slug: store.Str(r[4]),
			fileModel: store.Str(r[5])}
	}
	return out, nil
}

// syncExternalModels makes the managed services match services. A name the
// user's own service has is skipped: the user's always wins. What the entry
// itself rules out is skipped until the file changes; any other failure (a
// locked keychain) is returned, after the other entries, so the file is read
// again.
func (s *Service) syncExternalModels(ctx context.Context, services map[string]externalService) (failed error) {
	have, err := s.managedServices(ctx)
	if err != nil {
		return err
	}
	changed := false
	defer func() {
		if err := s.loadExtModels(ctx); err != nil {
			slog.Warn("external model info", "err", err)
		}
		if changed {
			s.bus.Publish(events.Event{Type: events.ModelsChanged})
		}
	}()
	for name, row := range have {
		if _, listed := services[name]; row.by == "" || listed {
			continue
		}
		if err := s.dropManagedService(ctx, row); err != nil {
			return err
		}
		changed = true
	}
	for _, name := range slices.Sorted(maps.Keys(services)) {
		e := services[name]
		if e.bad != nil {
			slog.Warn("external model service unreadable, left as it was", "name", name, "err", e.bad)
			continue
		}
		if e.BaseURL == "" && e.APIKey == "" { // an emptied entry, as one written key by key is left on disconnect
			if row, ok := have[name]; ok && row.by != "" {
				if err := s.dropManagedService(ctx, row); err != nil {
					return err
				}
				changed = true
			}
			continue
		}
		if err := checkExternalService(name, &e); err != nil {
			slog.Warn("external model service skipped", "name", name, "err", err)
			continue
		}
		row, exists := have[name]
		if exists && row.by == "" {
			slog.Warn("external model service skipped: the user has a service of that name", "name", name)
			continue
		}
		if !exists {
			if err := s.checkNameFree(ctx, name, 0); err != nil {
				slog.Warn("external model service skipped", "name", name, "err", err)
				continue
			}
		}
		in := ModelServiceInput{Name: name, Kind: e.Kind, BaseURL: e.BaseURL, APIKey: e.APIKey, KeyAction: KeyReplace,
			Models: e.ids(), Provider: ProviderOther, managedBy: e.By, ownModels: e.ownModels(),
			managedHash: entryHash([]any{e.By, e.BaseURL, e.Kind, e.Models, sha256.Sum256([]byte(e.APIKey))})}
		switch {
		case !exists:
			m, err := s.AddModelService(ctx, in)
			if err != nil {
				failed = errors.Join(failed, fmt.Errorf("add %s: %w", name, err))
				continue
			}
			row = managedRow{id: m.ID, by: e.By, slug: m.Slug}
			changed = true
		case row.hash != in.managedHash:
			if row.by != e.By { // a different gateway took the name over
				if _, err := s.db.DurableExecute(ctx, "UPDATE model_services SET managed_by = ? WHERE id = ?", e.By, row.id); err != nil {
					return err
				}
				row.by = e.By
			}
			if _, err := s.updateManagedService(ctx, row.id, in); err != nil {
				failed = errors.Join(failed, fmt.Errorf("update %s: %w", name, err))
				continue
			}
			changed = true
		}
		if err := s.setFileModel(ctx, row, e.Model); err != nil {
			return err
		}
	}
	return failed
}

func (s *Service) updateManagedService(ctx context.Context, id int64, in ModelServiceInput) (ModelService, error) {
	return s.UpdateModelService(ctx, id, in) // in.managedBy lets it through
}

// setFileModel records the model the file chooses for row's service; the
// settings read it as the default model while it is set (gatewayDefault).
func (s *Service) setFileModel(ctx context.Context, row managedRow, model string) error {
	if model == row.fileModel {
		return nil
	}
	if _, err := s.db.DurableExecute(ctx, "UPDATE model_services SET file_model = ? WHERE id = ?", model, row.id); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.SettingsChanged}) // the default model the settings show changed
	return nil
}

// dropManagedService removes row's service; if the file chose one of its
// models, the default model is the user's own again (gatewayDefault).
func (s *Service) dropManagedService(ctx context.Context, row managedRow) error {
	if err := s.removeModelService(ctx, row.id, row.by); err != nil {
		return err
	}
	if row.fileModel != "" {
		s.bus.Publish(events.Event{Type: events.SettingsChanged})
	}
	return nil
}

// gatewayDefault is the default model a gateway's file chooses, as
// "slug/model", and the gateway; "" when none does. It is read, never
// copied into the settings: the gateway keeps and restores its own file, so
// once the file stops choosing, the user's own default is the default again.
// Of several, the first by service name.
func (s *Service) gatewayDefault(ctx context.Context) (ref, by string, err error) {
	rows, err := s.db.Query(ctx, `SELECT slug, file_model, managed_by FROM model_services
		WHERE managed_by != '' AND file_model != '' ORDER BY name LIMIT 1`)
	if err != nil || len(rows.Values) == 0 {
		return "", "", err
	}
	r := rows.Values[0]
	return store.Str(r[0]) + "/" + store.Str(r[1]), store.Str(r[2]), nil
}

// syncExternalMCP makes the managed MCP servers match servers. New ones come
// without a grant: the user grants each before it runs (ADR 0004).
func (s *Service) syncExternalMCP(ctx context.Context, servers map[string]externalMCPServer, by string) (failed error) {
	list, err := s.mcpRows(ctx, 0)
	if err != nil {
		return err
	}
	hashes, err := s.mcpHashes(ctx)
	if err != nil {
		return err
	}
	have := map[string]MCPServer{}
	for _, srv := range list {
		have[srv.Name] = srv
		if _, listed := servers[srv.Name]; srv.ManagedBy != "" && !listed {
			if err := s.removeMCPServer(ctx, srv.ID, srv.ManagedBy); err != nil {
				return err
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		e := servers[name]
		if name != strings.TrimSpace(name) { // saved trimmed, it would never match its entry again
			slog.Warn("external MCP server skipped: spaces around its name", "name", name)
			continue
		}
		in := MCPServerInput{Name: name, Command: e.Command, Args: e.Args, Env: nonEmpty(e.Env), URL: e.URL, Headers: nonEmpty(e.Headers),
			managedBy: by, managedHash: entryHash(e)}
		switch {
		case e.Command != "":
			in.Transport = "stdio"
		case e.Type == "" || e.Type == "http" || e.Type == "streamable-http":
			in.Transport = "http"
		default:
			slog.Warn("external MCP server skipped: unsupported transport", "name", name, "type", e.Type)
			continue
		}
		old, exists := have[name]
		var was *MCPServer
		if exists {
			was = &old
		}
		if probe := in; exists && old.ManagedBy == "" {
			slog.Warn("external MCP server skipped: the user has a server of that name", "name", name)
			continue
		} else if err := checkMCPInput(&probe, was); err != nil { // the entry's own fault: skipped until the file changes
			slog.Warn("external MCP server skipped", "name", name, "err", err)
			continue
		}
		switch {
		case !exists:
			if _, err := s.AddMCPServer(ctx, in); err != nil {
				failed = errors.Join(failed, fmt.Errorf("add %s: %w", name, err))
			}
		case hashes[old.ID] != in.managedHash:
			if old.ManagedBy != by {
				if _, err := s.db.DurableExecute(ctx, "UPDATE mcp_servers SET managed_by = ? WHERE id = ?", by, old.ID); err != nil {
					return err
				}
			}
			if _, err := s.UpdateMCPServer(ctx, old.ID, in); err != nil { // its grant goes: what it runs changed
				failed = errors.Join(failed, fmt.Errorf("update %s: %w", name, err))
			}
		}
	}
	return failed
}

// nonEmpty is m without its empty values. A file's values are values: an
// empty one is no value, where a user's edit means "keep the saved one".
func nonEmpty(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func (s *Service) mcpHashes(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.Query(ctx, "SELECT id, managed_hash FROM mcp_servers")
	if err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for _, r := range rows.Values {
		out[store.Int(r[0])] = store.Str(r[1])
	}
	return out, nil
}

// externalInstructionsBlock is the user's standing instructions from
// ExternalDir/AGENTS.md for Wit's system prompt, escaped so they stay one
// block; "" when there are none.
func (s *Service) externalInstructionsBlock() string {
	if s.opts.ExternalDir == "" {
		return ""
	}
	f, err := os.Open(filepath.Join(s.opts.ExternalDir, externalInstructions))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("external instructions unreadable", "err", err)
		}
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxInstructions))
	if err != nil {
		slog.Warn("external instructions unreadable", "err", err)
		return ""
	}
	n := len(b)
	for n > 0 && !utf8.Valid(b[:n]) { // cut where a character was split
		n--
	}
	text := strings.TrimSpace(string(b[:n]))
	if text == "" {
		return ""
	}
	return "\n\nThe user's standing instructions, kept in " + html.EscapeString(s.managedBy(true)) +
		". Follow them where they do not conflict with the rules above:\n<user_instructions>\n" + html.EscapeString(text) + "\n</user_instructions>"
}

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
	By      string            `json:"by"`
	BaseURL string            `json:"baseURL"`
	APIKey  string            `json:"apiKey"`
	Kind    string            `json:"kind"`
	Models  []string          `json:"models"`
	Model   string            `json:"model"`
	Names   map[string]string `json:"names"`    // model ID -> the gateway's display name
	Context map[string]int    `json:"contexts"` // model ID -> its context window in tokens
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
	if s.extSeen == nil || s.extDirty.Swap(false) {
		s.extSeen = map[string]string{}
	}
	models, mChanged, mErr := s.readExternal(externalModels)
	var services map[string]externalService
	if mErr == nil && models != nil {
		mErr = json.Unmarshal(models, &services)
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
			slog.Warn("external models", "err", err)
		} else {
			s.extSeen[externalModels] = s.stamp(externalModels)
		}
	}
	mcp, cChanged, cErr := s.readExternal(externalMCP)
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
		slog.Warn("external MCP servers", "err", err)
		return
	}
	s.extSeen[externalMCP] = s.stamp(externalMCP)
}

// stamp is a file's size and modification time, "" when it is missing.
func (s *Service) stamp(name string) string {
	st, err := os.Stat(filepath.Join(s.opts.ExternalDir, name))
	if err != nil {
		return ""
	}
	return fmt.Sprint(st.Size(), st.ModTime().UnixNano())
}

// readExternal reads file name if its stamp changed: nil data for a missing
// file, which lists nothing.
func (s *Service) readExternal(name string) (data []byte, changed bool, err error) {
	st := s.stamp(name)
	if seen, ok := s.extSeen[name]; ok && seen == st {
		return nil, false, nil
	}
	if st == "" {
		return nil, true, nil
	}
	p := filepath.Join(s.opts.ExternalDir, name)
	info, err := os.Stat(p)
	if err == nil && info.Size() > maxExternalFile {
		err = fmt.Errorf("%s is larger than %d bytes", name, maxExternalFile)
	}
	if err == nil {
		data, err = os.ReadFile(p)
	}
	return data, true, err
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
	id                     int64
	by, hash, slug         string
	fileModel, prevDefault string
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
	case slices.ContainsFunc(e.Models, func(m string) bool {
		return m == "" || len(m) > 200 || strings.ContainsFunc(m, unicode.IsControl)
	}):
		return errors.New("a model id is empty, too long or has control characters")
	case e.Model != "" && !slices.Contains(e.Models, e.Model):
		return fmt.Errorf("model %q is not among its models", e.Model)
	}
	for id, n := range e.Names { // only a name is lost, never the service
		if !slices.Contains(e.Models, id) || n == "" || len(n) > 200 || strings.ContainsFunc(n, unicode.IsControl) {
			delete(e.Names, id)
		}
	}
	for id, c := range e.Context {
		if !slices.Contains(e.Models, id) || c < 1000 || c > 100_000_000 {
			delete(e.Context, id)
		}
	}
	return nil
}

// ownModels is what e says of its models, nil when nothing.
func (e externalService) ownModels() map[string]OwnModel {
	var out map[string]OwnModel
	for _, id := range e.Models {
		if m := (OwnModel{Name: e.Names[id], Context: e.Context[id]}); m != (OwnModel{}) {
			if out == nil {
				out = map[string]OwnModel{}
			}
			out[id] = m
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
	rows, err := s.db.Query(ctx, "SELECT id, name, managed_by, managed_hash, slug, file_model, prev_default FROM model_services")
	if err != nil {
		return nil, err
	}
	out := map[string]managedRow{}
	for _, r := range rows.Values {
		out[store.Str(r[1])] = managedRow{id: store.Int(r[0]), by: store.Str(r[2]), hash: store.Str(r[3]), slug: store.Str(r[4]),
			fileModel: store.Str(r[5]), prevDefault: store.Str(r[6])}
	}
	return out, nil
}

// syncExternalModels makes the managed services match services. A name the
// user's own service has is skipped: the user's always wins.
func (s *Service) syncExternalModels(ctx context.Context, services map[string]externalService) error {
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
		if err := s.removeModelService(ctx, row.id, row.by); err != nil {
			return err
		}
		changed = true
		if err := s.followExternalModel(ctx, row, ""); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(services)) {
		e := services[name]
		if e.BaseURL == "" && e.APIKey == "" { // what a disconnect leaves of the entry
			if row, ok := have[name]; ok && row.by != "" {
				if err := s.removeModelService(ctx, row.id, row.by); err != nil {
					return err
				}
				changed = true
				if err := s.followExternalModel(ctx, row, ""); err != nil {
					return err
				}
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
		in := ModelServiceInput{Name: name, Kind: e.Kind, BaseURL: e.BaseURL, APIKey: e.APIKey, KeyAction: KeyReplace,
			Models: e.Models, Provider: ProviderOther, managedBy: e.By, ownModels: e.ownModels(),
			managedHash: entryHash([]any{e.By, e.BaseURL, e.Kind, e.Models, e.Names, e.Context, sha256.Sum256([]byte(e.APIKey))})}
		switch {
		case !exists:
			m, err := s.AddModelService(ctx, in)
			if err != nil {
				slog.Warn("external model service skipped", "name", name, "err", err)
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
				slog.Warn("external model service not updated", "name", name, "err", err)
				continue
			}
			changed = true
		}
		if err := s.followExternalModel(ctx, row, e.Model); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) updateManagedService(ctx context.Context, id int64, in ModelServiceInput) (ModelService, error) {
	return s.UpdateModelService(ctx, id, in) // in.managedBy lets it through
}

// followExternalModel sets the default model when the file chooses a new one,
// and gives the user's back when it chooses none or the service goes. Only a
// change in the file moves it: a default the user picked since stays.
func (s *Service) followExternalModel(ctx context.Context, row managedRow, model string) error {
	if model == row.fileModel {
		return nil
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	prefix := row.slug + "/"
	ours := strings.HasPrefix(st.DefaultModel, prefix)
	prev := row.prevDefault
	switch {
	case model != "":
		if !ours {
			prev = st.DefaultModel
		}
		st.DefaultModel = prefix + model
	case ours:
		st.DefaultModel, prev = prev, ""
	default:
		prev = ""
	}
	if st.DefaultModel != "" && !strings.HasPrefix(st.DefaultModel, prefix) && !s.modelRefExists(ctx, st.DefaultModel) {
		st.DefaultModel = "" // the user's earlier default went meanwhile
	}
	if _, err := s.SetSettings(ctx, st); err != nil {
		return err
	}
	_, err = s.db.DurableExecute(ctx, "UPDATE model_services SET file_model = ?, prev_default = ? WHERE id = ?", model, prev, row.id)
	return err
}

// modelRefExists reports whether ref still names a model.
func (s *Service) modelRefExists(ctx context.Context, ref string) bool {
	_, _, err := s.resolveRef(ctx, ref)
	return !errors.Is(err, ErrModelServiceNotFound) && !errors.Is(err, ErrModelNotFound)
}

// syncExternalMCP makes the managed MCP servers match servers. New ones come
// without a grant: the user grants each before it runs (ADR 0004).
func (s *Service) syncExternalMCP(ctx context.Context, servers map[string]externalMCPServer, by string) error {
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
		in := MCPServerInput{Name: name, Command: e.Command, Args: e.Args, Env: e.Env, URL: e.URL, Headers: e.Headers,
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
		switch {
		case exists && old.ManagedBy == "":
			slog.Warn("external MCP server skipped: the user has a server of that name", "name", name)
		case !exists:
			if _, err := s.AddMCPServer(ctx, in); err != nil {
				slog.Warn("external MCP server skipped", "name", name, "err", err)
			}
		case hashes[old.ID] != in.managedHash:
			if old.ManagedBy != by {
				if _, err := s.db.DurableExecute(ctx, "UPDATE mcp_servers SET managed_by = ? WHERE id = ?", by, old.ID); err != nil {
					return err
				}
			}
			if _, err := s.UpdateMCPServer(ctx, old.ID, in); err != nil { // its grant goes: what it runs changed
				slog.Warn("external MCP server not updated", "name", name, "err", err)
			}
		}
	}
	return nil
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

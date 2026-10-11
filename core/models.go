package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
	"github.com/zalando/go-keyring"
)

var (
	errNoModels      = errors.New("add at least one model")
	errNoJudgeModel  = errors.New("choose a judge model")
	errNoCapability  = errors.New("turn on generate or judge")
	errCapabilityOff = errors.New("that capability is off for this service")
)

var (
	ErrAINotConfigured      = errors.New("no model configured")
	ErrModelServiceNotFound = errors.New("model service not found")
	ErrModelNotFound        = errors.New("model not found")
)

// ModelService is a model service and its two capabilities. Kind is the
// generate format: ai.KindOpenAI (chat), ai.KindResponses or ai.KindAnthropic
// (messages); "" turns generate off. JudgeFormat is ai.FormatDecisions or
// ai.FormatSystemOne; "" turns judge off. BaseURL is one base for both,
// normalized the same for every format.
type ModelService struct {
	ID                      int64
	Name, Kind, BaseURL     string
	Provider                string // ProviderOpenAI, …: the formats it offers; fixed once added
	Slug                    string // the service part of its "slug/model" references; kept on rename
	Models                  []string
	JudgeFormat, JudgeModel string
	HasKey                  bool
	Version                 int64
	// Status is the last test of each capability (CapGen, CapJudge), absent
	// when untested or when what it tested changed since.
	Status     map[string]CapStatus
	keyVersion int64 // its key's keychain entry (model_services.key_version)
}

// Providers: which formats a service offers (the add dialog's tiles).
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderTypeSafe  = "typesafe"
	ProviderOther     = "other"
)

var providerFormats = map[string]struct{ gen, judge []string }{
	ProviderOpenAI:    {[]string{ai.KindResponses, ai.KindOpenAI}, []string{string(ai.FormatDecisions)}},
	ProviderAnthropic: {[]string{ai.KindAnthropic}, nil},
	ProviderTypeSafe:  {nil, []string{string(ai.FormatSystemOne)}},
	ProviderOther:     {[]string{ai.KindOpenAI, ai.KindResponses, ai.KindAnthropic}, []string{string(ai.FormatDecisions), string(ai.FormatSystemOne)}},
}

// ProviderFormats are the generate kinds and judge formats provider p offers, in the dialog's order.
func ProviderFormats(p string) (gen, judge []string) {
	f := providerFormats[p]
	return slices.Clone(f.gen), slices.Clone(f.judge)
}

// inferProvider is the provider of a service added without one, as the
// migration infers it for older services: by host, else other.
func inferProvider(base, kind string) string {
	p := ProviderOther
	if u, err := url.Parse(base); err == nil {
		p = cmp.Or(map[string]string{"api.openai.com": ProviderOpenAI, "api.anthropic.com": ProviderAnthropic}[u.Host], p)
	}
	if kind != "" && !slices.Contains(providerFormats[p].gen, kind) {
		return ProviderOther
	}
	return p
}

// Capabilities, the keys of ModelService.Status.
const (
	CapGen   = "gen"
	CapJudge = "judge"
)

// CapStatus is a capability's last test. It holds codes, never error text:
// errors can echo a key, a response body or mail.
type CapStatus struct {
	State      string // "ok" or "fail"
	LatencyMs  int
	HTTPStatus int    // the status that failed it, when known
	Code       string // why it failed: auth, forbidden, no_list, timeout, bad_answer, error, unreachable
	At         time.Time
}

// Key actions for ModelServiceInput.KeyAction.
const (
	KeyKeep    = "keep"    // reuse the stored key; only while kind and address are unchanged
	KeyReplace = "replace" // use APIKey
	KeyClear   = "clear"   // no key
)

var (
	ErrModelAuth       = ai.ErrModelAuth
	ErrBaseURLInvalid  = ai.ErrBaseURLInvalid
	ErrBaseURLScheme   = ai.ErrBaseURLScheme
	ErrKeyNeedsConfirm = errors.New("the address or protocol changed: enter the key again or choose no key")
)

// ModelServiceInput is a service as the user entered it. An empty KeyAction
// means replace when APIKey is set, else keep (update) or none (add).
// Provider "" is the stored one, or for a new service the one its address
// implies (inferProvider). A capability that is on needs its models.
type ModelServiceInput struct {
	Name, Kind, BaseURL, APIKey string
	KeyAction                   string
	Models                      []string
	Provider                    string
	JudgeFormat, JudgeModel     string
}

// Input is m as it is saved, keeping its key.
func (m ModelService) Input() ModelServiceInput {
	return ModelServiceInput{Name: m.Name, Kind: m.Kind, BaseURL: m.BaseURL, KeyAction: KeyKeep, Models: slices.Clone(m.Models),
		Provider: m.Provider, JudgeFormat: m.JudgeFormat, JudgeModel: m.JudgeModel}
}

// complete: each capability that is on names its models.
func (in ModelServiceInput) complete() error {
	if in.Kind != "" && len(in.Models) == 0 {
		return errNoModels
	}
	if in.JudgeFormat != "" && in.JudgeModel == "" {
		return errNoJudgeModel
	}
	return nil
}

// modelKeyUser names key entry v of service id: v 1 is the unversioned entry
// of older Godwits.
func (s *Service) modelKeyUser(id, v int64) string {
	u := s.installID + "/model:" + strconv.FormatInt(id, 10)
	if v > 1 {
		u += ":v" + strconv.FormatInt(v, 10)
	}
	return u
}

// modelKey reads m's key from the entry its row names; "" for none.
func (s *Service) modelKey(m ModelService) (string, error) {
	if m.keyVersion == 0 {
		return "", nil
	}
	k, err := keychainGet(s.modelKeyUser(m.ID, m.keyVersion))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return k, err
}

// dropKey deletes key entry v of service id, then its trash row. Best effort:
// what fails stays in model_key_trash for the next start (sweepModelKeys).
func (s *Service) dropKey(ctx context.Context, id, v int64) {
	if err := keychainDelete(s.modelKeyUser(id, v)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		slog.Warn("model service key entry left for the next start", "service", id, "version", v)
		return
	}
	if _, err := s.db.Execute(ctx, "DELETE FROM model_key_trash WHERE service_id = ? AND key_version = ?", id, v); err != nil {
		slog.Warn("model service key trash", "service", id, "version", v, "err", err)
	}
}

// sweepModelKeys deletes the key entries a crash or a keychain failure left
// behind; never the one a service uses.
func (s *Service) sweepModelKeys(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT t.service_id, t.key_version, m.key_version FROM model_key_trash t
		LEFT JOIN model_services m ON m.id = t.service_id`)
	if err != nil {
		return err
	}
	for _, r := range rows.Values {
		id, v := store.Int(r[0]), store.Int(r[1])
		if r[2] != nil && store.Int(r[2]) == v { // in use: only the trash row goes
			if _, err := s.db.Execute(ctx, "DELETE FROM model_key_trash WHERE service_id = ? AND key_version = ?", id, v); err != nil {
				return err
			}
			continue
		}
		s.dropKey(ctx, id, v)
	}
	return nil
}

// prepare validates in, normalizes its address, and resolves its key action
// against service id (0: a new one, old empty) into the key requests should
// carry. The caller holds modelMu. A stored key is only kept for the address
// it was saved with, and not across a switch from one format to another:
// keys only go to the address they belong to.
func (s *Service) prepare(ctx context.Context, id int64, in *ModelServiceInput) (action, key string, old ModelService, err error) {
	if in.Name == "" || strings.Contains(in.Name, "/") {
		return "", "", old, errors.New("service name must be non-empty and contain no \"/\"")
	}
	if id != 0 {
		if old, err = s.modelServiceBy(ctx, "id = ?", id); err != nil {
			return "", "", old, err
		}
	}
	if in.BaseURL, err = ai.NormalizeBaseURL(in.BaseURL); err != nil {
		return "", "", old, err
	}
	if in.Provider == "" {
		in.Provider = cmp.Or(old.Provider, inferProvider(in.BaseURL, in.Kind))
	}
	f, ok := providerFormats[in.Provider]
	switch {
	case !ok:
		return "", "", old, fmt.Errorf("invalid provider %q", in.Provider)
	case id != 0 && in.Provider != old.Provider:
		return "", "", old, errors.New("a service's provider can't change")
	case in.Kind == "" && in.JudgeFormat == "":
		return "", "", old, errNoCapability
	case in.Kind != "" && !slices.Contains(f.gen, in.Kind):
		return "", "", old, fmt.Errorf("invalid service kind %q for %s", in.Kind, in.Provider)
	case in.JudgeFormat != "" && !slices.Contains(f.judge, in.JudgeFormat):
		return "", "", old, fmt.Errorf("invalid judge format %q for %s", in.JudgeFormat, in.Provider)
	}
	if in.JudgeFormat == "" {
		in.JudgeModel = ""
	}
	action = in.KeyAction
	if action == "" {
		action = KeyKeep
		if in.APIKey != "" {
			action = KeyReplace
		} else if id == 0 {
			action = KeyClear
		}
	}
	switch action {
	case KeyReplace:
		if in.APIKey == "" {
			return "", "", old, errors.New("enter an API key or choose no key")
		}
		return action, in.APIKey, old, nil
	case KeyClear:
		return action, "", old, nil
	case KeyKeep:
		if id == 0 {
			return "", "", old, errors.New("a new service has no stored key to keep")
		}
		key, err := s.modelKey(old)
		if err != nil {
			return "", "", old, err
		}
		if key == "" {
			return action, "", old, nil // nothing to send anywhere
		}
		// Turning a capability on or off sends the key nowhere new; switching formats might.
		switched := func(was, is string) bool { return was != "" && is != "" && was != is }
		oldURL, nerr := ai.NormalizeBaseURL(old.BaseURL)
		if nerr != nil || oldURL != in.BaseURL || switched(old.Kind, in.Kind) || switched(old.JudgeFormat, in.JudgeFormat) {
			return "", "", old, ErrKeyNeedsConfirm
		}
		return action, key, old, nil
	}
	return "", "", old, fmt.Errorf("invalid key action %q", action)
}

const modelCols = "id, name, kind, base_url, models, version, slug, key_version, provider, judge_format, judge_model"

func (s *Service) scanModelService(r []any) (ModelService, error) {
	m := ModelService{ID: store.Int(r[0]), Name: store.Str(r[1]), Kind: store.Str(r[2]), BaseURL: store.Str(r[3]), Version: store.Int(r[5]), Slug: store.Str(r[6]), keyVersion: store.Int(r[7]),
		Provider: store.Str(r[8]), JudgeFormat: store.Str(r[9]), JudgeModel: store.Str(r[10])}
	if err := json.Unmarshal([]byte(store.Str(r[4])), &m.Models); err != nil {
		return m, fmt.Errorf("stored models: %w", err)
	}
	k, kerr := s.modelKey(m) // a keychain error is just "no key" here
	m.HasKey = kerr == nil && k != ""
	return m, nil
}

func (s *Service) modelServiceBy(ctx context.Context, where string, arg any) (ModelService, error) {
	rows, err := s.db.Query(ctx, "SELECT "+modelCols+" FROM model_services WHERE "+where, arg)
	if err != nil {
		return ModelService{}, err
	}
	if len(rows.Values) == 0 {
		return ModelService{}, fmt.Errorf("%w: %v", ErrModelServiceNotFound, arg)
	}
	return s.scanModelService(rows.Values[0])
}

// ListModelServices lists the services in the order they were added.
func (s *Service) ListModelServices(ctx context.Context) ([]ModelService, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT "+modelCols+" FROM model_services ORDER BY id")
	if err != nil {
		return nil, err
	}
	st, err := s.capStatuses(ctx)
	if err != nil {
		return nil, err
	}
	out := []ModelService{}
	for _, r := range rows.Values {
		m, err := s.scanModelService(r)
		if err != nil {
			return nil, err
		}
		m.Status = st[m.ID]
		out = append(out, m)
	}
	return out, nil
}

// capStatuses is every service's ModelService.Status.
func (s *Service) capStatuses(ctx context.Context) (map[int64]map[string]CapStatus, error) {
	rows, err := s.db.Query(ctx, "SELECT service_id, cap, state, latency_ms, http_status, code, at FROM model_service_status")
	if err != nil {
		return nil, err
	}
	out := map[int64]map[string]CapStatus{}
	for _, r := range rows.Values {
		id := store.Int(r[0])
		if out[id] == nil {
			out[id] = map[string]CapStatus{}
		}
		out[id][store.Str(r[1])] = CapStatus{State: store.Str(r[2]), LatencyMs: int(store.Int(r[3])), HTTPStatus: int(store.Int(r[4])),
			Code: store.Str(r[5]), At: time.UnixMilli(store.Int(r[6]))}
	}
	return out, nil
}

func modelsJSON(m []string) string {
	if m == nil {
		m = []string{}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// AddModelService stores the service, then its key in the keychain (entry 1);
// if the keychain fails the row is removed again. A crash between the two
// leaves a service without a key.
func (s *Service) AddModelService(ctx context.Context, in ModelServiceInput) (ModelService, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ModelService{}, err
	}
	defer end()
	if err := in.complete(); err != nil {
		return ModelService{}, err
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	_, key, _, err := s.prepare(ctx, 0, &in)
	if err != nil {
		return ModelService{}, err
	}
	if err := s.checkNameFree(ctx, in.Name, 0); err != nil {
		return ModelService{}, err
	}
	slug, err := s.newSlug(ctx, in.Name, 0)
	if err != nil {
		return ModelService{}, err
	}
	kv := 0
	if key != "" {
		kv = 1
	}
	res, err := s.db.Execute(ctx, `INSERT INTO model_services(name, kind, base_url, models, slug, created_at, key_version, provider, judge_format, judge_model)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Kind, in.BaseURL, modelsJSON(in.Models), slug, time.Now().Unix(), kv, in.Provider, in.JudgeFormat, in.JudgeModel)
	if err != nil {
		return ModelService{}, err
	}
	id := res.LastInsertID
	if key != "" {
		if err := keychainSet(s.modelKeyUser(id, 1), key); err != nil {
			uctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, derr := s.db.Transaction(uctx, []sqlite.Statement{
				{SQL: "DELETE FROM model_services WHERE id = ?", Args: []any{id}},
				{SQL: "INSERT OR IGNORE INTO model_key_trash(service_id, key_version) VALUES (?, 1)", Args: []any{id}},
			}); derr != nil {
				err = errors.Join(err, derr)
			}
			return ModelService{}, fmt.Errorf("save API key to keychain: %w", err)
		}
	}
	return s.modelServiceBy(ctx, "id = ?", id)
}

// UpdateModelService replaces the service's fields, applies the key action and
// bumps its version. A new key goes to a new keychain entry first; then one
// transaction switches the address and the entry together; then the old entry
// is deleted. A crash at any point never pairs a key with an address it was
// not saved for: the row names the old entry or the new one, never a
// half-written one, and the next start deletes the other (sweepModelKeys).
func (s *Service) UpdateModelService(ctx context.Context, id int64, in ModelServiceInput) (ModelService, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ModelService{}, err
	}
	defer end()
	if err := in.complete(); err != nil {
		return ModelService{}, err
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	action, key, old, err := s.prepare(ctx, id, &in)
	if err != nil {
		return ModelService{}, err
	}
	if err := s.checkNameFree(ctx, in.Name, id); err != nil {
		return ModelService{}, err
	}
	trash := "INSERT OR IGNORE INTO model_key_trash(service_id, key_version) VALUES (?, ?)"
	kv := old.keyVersion
	switch action {
	case KeyReplace:
		kv = old.Version + 1 // the new version's own entry: never one a row named before
		// Listed first: until the switch below commits, nothing else knows of it.
		if _, err := s.db.Execute(ctx, trash, id, kv); err != nil {
			return ModelService{}, err
		}
		if err := keychainSet(s.modelKeyUser(id, kv), key); err != nil {
			return ModelService{}, fmt.Errorf("save API key to keychain: %w", err)
		}
		// The key is written: a cancelled ctx must not leave the update half done.
		ctx = context.WithoutCancel(ctx)
	case KeyClear:
		kv = 0
	}
	stmts := []sqlite.Statement{{SQL: `UPDATE model_services SET name = ?, kind = ?, base_url = ?, models = ?, version = version + 1, key_version = ?,
		judge_format = ?, judge_model = ? WHERE id = ?`,
		Args: []any{in.Name, in.Kind, in.BaseURL, modelsJSON(in.Models), kv, in.JudgeFormat, in.JudgeModel, id}}}
	// A test's status no longer holds once what it tested changed: the
	// address or key for both capabilities, a format (or judge model) for its own.
	moved := in.BaseURL != old.BaseURL || action == KeyReplace || action == KeyClear && old.keyVersion != 0
	for c, changed := range map[string]bool{CapGen: in.Kind != old.Kind, CapJudge: in.JudgeFormat != old.JudgeFormat || in.JudgeModel != old.JudgeModel} {
		if moved || changed {
			stmts = append(stmts, sqlite.Statement{SQL: "DELETE FROM model_service_status WHERE service_id = ? AND cap = ?", Args: []any{id, c}})
		}
	}
	if kv != old.keyVersion {
		stmts = append(stmts, sqlite.Statement{SQL: "DELETE FROM model_key_trash WHERE service_id = ? AND key_version = ?", Args: []any{id, kv}})
		if old.keyVersion != 0 {
			stmts = append(stmts, sqlite.Statement{SQL: trash, Args: []any{id, old.keyVersion}})
		}
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return ModelService{}, err // a new key's entry stays listed in the trash
	}
	s.forgetModel(id)
	// The write is done: a cancelled ctx must not turn it into an error.
	ctx = context.WithoutCancel(ctx)
	if kv != old.keyVersion && old.keyVersion != 0 {
		s.dropKey(ctx, id, old.keyVersion)
	}
	m, err := s.modelServiceBy(ctx, "id = ?", id)
	if err != nil {
		return m, err
	}
	st, err := s.capStatuses(ctx)
	m.Status = st[id]
	return m, err
}

// checkNameFree refuses a name that is another service's slug or old name:
// references to that name resolve to that service, not to this one.
func (s *Service) checkNameFree(ctx context.Context, name string, id int64) error {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM model_services WHERE slug = ? AND id != ? UNION ALL SELECT 1 FROM model_service_aliases WHERE name = ? AND service_id != ?",
		name, id, name, id)
	if err != nil {
		return err
	}
	if len(rows.Values) > 0 {
		return fmt.Errorf("service name %q is another service's reference name", name)
	}
	return nil
}

func (s *Service) RemoveModelService(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	old, err := s.modelServiceBy(ctx, "id = ?", id)
	if errors.Is(err, ErrModelServiceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	stmts := []sqlite.Statement{{SQL: "DELETE FROM model_services WHERE id = ?", Args: []any{id}}}
	if old.keyVersion != 0 {
		stmts = append(stmts, sqlite.Statement{SQL: "INSERT OR IGNORE INTO model_key_trash(service_id, key_version) VALUES (?, ?)", Args: []any{id, old.keyVersion}})
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return err
	}
	s.forgetModel(id)
	if old.keyVersion != 0 {
		s.dropKey(context.WithoutCancel(ctx), id, old.keyVersion)
	}
	return nil
}

// FetchModels lists the service's models, sorted. It saves nothing.
func (s *Service) FetchModels(ctx context.Context, id int64) ([]string, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	return s.fetchModels(ctx, id)
}

func (s *Service) fetchModels(ctx context.Context, id int64) ([]string, error) {
	m, key, err := s.modelServiceKey(ctx, id)
	if err != nil {
		return nil, err
	}
	return ai.ListModels(ctx, s.modelHTTP, m.Kind, m.BaseURL, key)
}

// modelServiceKey reads the service and its key as one snapshot (modelMu).
func (s *Service) modelServiceKey(ctx context.Context, id int64) (ModelService, string, error) {
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	m, err := s.modelServiceBy(ctx, "id = ?", id)
	if err != nil {
		return m, "", err
	}
	key, err := s.modelKey(m)
	if err != nil {
		return m, "", err
	}
	return m, key, nil
}

// TestCapability tests capability c (CapGen, CapJudge) of in as service id
// (0: a new one) would be saved, without saving it: generate runs the
// shortest real generation on its first model (or the first one it lists),
// judge asks a yes/no about a fixed sample. A failed test returns its status
// and the error; an invalid in returns no status. When in is service id as
// saved (same address, stored key, and format), the status is stored, unless
// the service changed while the test ran.
func (s *Service) TestCapability(ctx context.Context, id int64, in ModelServiceInput, c string) (CapStatus, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return CapStatus{}, err
	}
	defer end()
	s.modelMu.RLock()
	action, key, old, err := s.prepare(ctx, id, &in)
	s.modelMu.RUnlock()
	if err != nil {
		return CapStatus{}, err
	}
	var same bool
	start := time.Now()
	switch {
	case c == CapGen && in.Kind != "":
		same = old.Kind == in.Kind
		err = s.testGen(ctx, id, in, key)
	case c == CapJudge && in.JudgeFormat != "":
		same = old.JudgeFormat == in.JudgeFormat && old.JudgeModel == in.JudgeModel
		err = s.testJudge(ctx, in, key)
	case c == CapGen, c == CapJudge:
		return CapStatus{}, errCapabilityOff
	default:
		return CapStatus{}, fmt.Errorf("unknown capability %q", c)
	}
	st := CapStatus{State: "ok", LatencyMs: int(time.Since(start).Milliseconds()), At: time.Now()}
	if err != nil {
		st.State = "fail"
		st.Code, st.HTTPStatus = capCode(err)
	}
	if id != 0 && same && action == KeyKeep && in.BaseURL == old.BaseURL && ctx.Err() == nil {
		// Only while the service is as tested: an update in between bumped the version.
		if _, werr := s.db.Execute(ctx, `INSERT INTO model_service_status(service_id, cap, state, latency_ms, http_status, code, at)
			SELECT ?, ?, ?, ?, ?, ?, ? WHERE (SELECT version FROM model_services WHERE id = ?) = ?
			ON CONFLICT(service_id, cap) DO UPDATE SET state = excluded.state, latency_ms = excluded.latency_ms,
				http_status = excluded.http_status, code = excluded.code, at = excluded.at`,
			id, c, st.State, st.LatencyMs, st.HTTPStatus, st.Code, st.At.UnixMilli(), id, old.Version); werr != nil {
			slog.Warn("model service status", "service", id, "err", werr)
		}
	}
	return st, err
}

// genTestTimeout bounds a generation test; tests shorten it.
var genTestTimeout = 30 * time.Second

func (s *Service) testGen(ctx context.Context, id int64, in ModelServiceInput, key string) error {
	tctx, cancel := context.WithTimeout(ctx, genTestTimeout)
	defer cancel()
	err := s.testGenRun(tctx, id, in, key)
	if err != nil && ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the model service did not answer in time: %w", context.DeadlineExceeded)
	}
	return err
}

func (s *Service) testGenRun(ctx context.Context, id int64, in ModelServiceInput, key string) error {
	models := in.Models
	if len(models) == 0 {
		var err error
		if models, err = ai.ListModels(ctx, s.modelHTTP, in.Kind, in.BaseURL, key); err != nil {
			return err
		}
		if len(models) == 0 {
			return fmt.Errorf("%w: the service lists no models", ErrModelNotFound)
		}
	}
	// A throwaway engine: in may be unsaved, and must not replace the saved
	// service's instance (that would end its runs).
	e, err := ai.NewEngine(slog.Default(), s.secrets.Values)
	if err != nil {
		return err
	}
	defer e.Close()
	_, err = runText(ctx, e, ai.Request{Model: ai.ResolvedModel{ServiceID: id, Kind: in.Kind, BaseURL: in.BaseURL, Model: models[0], APIKey: key},
		Prompt: "Reply with OK.", MaxTurns: 1}, 0)
	return err
}

func (s *Service) testJudge(ctx context.Context, in ModelServiceInput, key string) error {
	f := ai.JudgeFormat(in.JudgeFormat)
	j, err := ai.NewJudge(s.modelHTTP, f, in.BaseURL, key, in.JudgeModel)
	if err != nil {
		return err
	}
	_, err = j.Ask(ctx, "Hi Sam, thanks for the notes. See you on Friday!",
		[]ai.Question{{Name: "greeting", Instructions: "Does the text greet someone?", Kind: ai.YesNo}})
	return err
}

// capCode is a failed test's CapStatus code and HTTP status.
func capCode(err error) (string, int) {
	var ae *ai.Error
	var he *ai.HTTPError
	var nl *ai.NoListError
	run := errors.As(err, &ae)
	switch {
	case errors.Is(err, ai.ErrModelAuth), run && ae.Code == ai.CodeUnauthorized:
		return "auth", 401
	case errors.Is(err, ai.ErrModelForbidden):
		return "forbidden", 403
	case errors.As(err, &nl):
		return "no_list", nl.Status
	case errors.Is(err, ErrModelNotFound):
		return "no_list", 0
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", 0
	case errors.Is(err, ai.ErrJudgeAnswer), run && (ae.Code == ai.CodeBadResponse || ae.Code == ai.CodeIncomplete):
		return "bad_answer", 0
	case errors.As(err, &he):
		return "error", he.Status
	case run && ae.Code == ai.CodeRateLimited:
		return "error", 429
	}
	return "unreachable", 0
}

// runText runs req and returns its text, cut at max bytes (0: none kept).
func runText(ctx context.Context, e *ai.Engine, req ai.Request, max int) (string, error) {
	events, wait := e.Run(ctx, req)
	var b strings.Builder
	for ev := range events {
		if ev.Kind == "text" && b.Len() < max {
			b.WriteString(ev.Text[:min(len(ev.Text), max-b.Len())])
		}
	}
	if r := wait(); r.Err != nil {
		return "", r.Err
	}
	return b.String(), nil
}

// aiEngine creates the engine on first use, so GENKIT_ENV=dev fails only AI
// calls. The engine redacts every model key it is handed, and every extension
// secret in s.secrets, from Genkit's logs, tool errors and run errors.
func (s *Service) aiEngine() (*ai.Engine, error) {
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	if s.ai == nil {
		e, err := ai.NewEngine(slog.Default(), s.secrets.Values)
		if err != nil {
			return nil, err
		}
		// No modelMu here: an update holds it while it calls Forget.
		e.Current = func(ctx context.Context, id int64) (int64, error) {
			rows, err := s.db.Query(context.WithoutCancel(ctx), "SELECT version FROM model_services WHERE id = ?", id)
			if err != nil || len(rows.Values) == 0 {
				return 0, cmp.Or(err, ErrModelServiceNotFound)
			}
			return store.Int(rows.Values[0][0]), nil
		}
		s.ai = e
	}
	return s.ai, nil
}

// forgetModel drops the service's Genkit instance after a change or removal.
func (s *Service) forgetModel(id int64) {
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	if s.ai != nil {
		s.ai.Forget(id)
	}
}

// resolveModel resolves override, or Settings.DefaultModel when empty.
func (s *Service) resolveModel(ctx context.Context, override string) (ai.ResolvedModel, error) {
	m, _, err := s.resolveRef(ctx, override)
	return m, err
}

// resolveTask resolves a skill's or quick replies' model: override, else
// SmallModel when small, else DefaultModel. A SmallModel that is unset or names
// a removed service or model falls back to DefaultModel; other errors return.
func (s *Service) resolveTask(ctx context.Context, override string, small bool) (ai.ResolvedModel, string, error) {
	if override == "" && small {
		st, err := s.loadSettings(ctx)
		if err != nil {
			return ai.ResolvedModel{}, "", err
		}
		if st.SmallModel != "" {
			m, ref, err := s.resolveRef(ctx, st.SmallModel)
			if !errors.Is(err, ErrModelServiceNotFound) && !errors.Is(err, ErrModelNotFound) {
				return m, ref, err
			}
		}
	}
	return s.resolveRef(ctx, override)
}

// TaskModel is the "slug/model" skill runs on (quick replies when skill is
// ""), resolved as RunSkill and QuickReplies resolve it, without running it.
// ErrAINotConfigured, ErrModelServiceNotFound or ErrModelNotFound: it has none.
func (s *Service) TaskModel(ctx context.Context, skill string) (string, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return "", err
	}
	defer end()
	override, small := "", true
	if skill != "" {
		sk, err := s.skill(ctx, skill)
		if err != nil {
			return "", err
		}
		override, small = sk.Model, sk.Tier == "small"
	}
	_, ref, err := s.resolveTask(ctx, override, small)
	return ref, err
}

// resolveRef is resolveModel plus the reference in its "slug/model" form. The
// service part matches a slug, else an old name (references from before slugs).
func (s *Service) resolveRef(ctx context.Context, override string) (ai.ResolvedModel, string, error) {
	ref := override
	if ref == "" {
		st, err := s.loadSettings(ctx)
		if err != nil {
			return ai.ResolvedModel{}, "", err
		}
		if ref = st.DefaultModel; ref == "" {
			return ai.ResolvedModel{}, "", ErrAINotConfigured
		}
	}
	r, err := ai.ParseModelRef(ref)
	if err != nil {
		return ai.ResolvedModel{}, "", err
	}
	m, key, err := s.serviceByRef(ctx, r.Service)
	if err != nil {
		return ai.ResolvedModel{}, "", err
	}
	if m.Kind == "" || !slices.Contains(m.Models, r.Model) { // generate off: its models don't resolve
		return ai.ResolvedModel{}, "", fmt.Errorf("%w: %s", ErrModelNotFound, ref)
	}
	return ai.ResolvedModel{ServiceID: m.ID, Version: m.Version, Kind: m.Kind, BaseURL: m.BaseURL, Model: r.Model, APIKey: key},
		m.Slug + "/" + r.Model, nil
}

// serviceByRef is the service a reference's service part names, a slug or
// else an old name, and its key: the row and the key as one snapshot, never
// one service's URL with another's key.
func (s *Service) serviceByRef(ctx context.Context, name string) (ModelService, string, error) {
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	m, err := s.modelServiceBy(ctx, "slug = ?", name)
	if errors.Is(err, ErrModelServiceNotFound) { // never by current name: it may have moved to another service
		m, err = s.modelServiceBy(ctx, "id = (SELECT service_id FROM model_service_aliases WHERE name = ?)", name)
	}
	if err != nil {
		return m, "", err
	}
	key, err := s.modelKey(m)
	return m, key, err
}

// resolveJudge is the judge Settings.JudgeService names, and its "slug/model"
// label. When that is unset, gone or has judge off, the default generation
// model answers instead (ai.NewGenJudge, gen true), as SmallModel falls back to it.
func (s *Service) resolveJudge(ctx context.Context) (j ai.Judge, label string, gen bool, err error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, "", false, err
	}
	if st.JudgeService != "" {
		m, key, err := s.serviceByRef(ctx, st.JudgeService)
		if err != nil && !errors.Is(err, ErrModelServiceNotFound) {
			return nil, "", false, err
		}
		if err == nil && m.JudgeFormat != "" {
			j, err := ai.NewJudge(s.modelHTTP, ai.JudgeFormat(m.JudgeFormat), m.BaseURL, key, m.JudgeModel)
			return j, m.Slug + "/" + m.JudgeModel, false, err
		}
	}
	model, ref, err := s.resolveRef(ctx, "")
	if err != nil {
		return nil, "", false, err
	}
	e, err := s.aiEngine()
	if err != nil {
		return nil, "", false, err
	}
	return ai.NewGenJudge(func(ctx context.Context, system, prompt string) (string, error) {
		return runText(ctx, e, ai.Request{Model: model, System: system, Prompt: prompt, MaxTurns: 1}, maxJudgeText)
	}), ref, true, nil
}

// maxJudgeText caps a generated judge answer: a JSON object of short answers.
const maxJudgeText = 64 << 10

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
)

// The directory (docs/adr/0032-plugin-directory-points-at-plugins.md): the
// plugins repository's directory.json says what each plugin is and pins, by
// commit, the godwit-plugin.json that says what it released. build joins them
// into the registry.json Godwit reads.

const maxPluginFile = 1 << 20

// Tests replace them.
var (
	sourceBase = "https://raw.githubusercontent.com"
	buildNow   = time.Now
)

var (
	repoRE      = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	commitRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	categories  = []string{plugins.CategoryTranslate, plugins.CategoryModels, plugins.CategoryOther}
	dataFlows   = []string{plugins.DataFlowLocal, plugins.DataFlowThirdParty}
	errNoSource = errors.New("source needs repo, path and a full commit sha")
)

type directoryEntry struct {
	Name      string          `json:"name"`
	Title     json.RawMessage `json:"title"`
	Summary   json.RawMessage `json:"summary"`
	Category  string          `json:"category"`
	Publisher string          `json:"publisher"`
	DataFlow  string          `json:"dataFlow"`
	Homepage  string          `json:"homepage"`
	Source    struct {
		Repo string `json:"repo"`
		Path string `json:"path"`
		SHA  string `json:"sha"`
	} `json:"source"`
	Revoked []string `json:"revoked,omitempty"` // versions taken back without the author's help
}

func build(directoryPath, outPath string) error {
	b, err := os.ReadFile(directoryPath)
	if err != nil {
		return err
	}
	var dir struct {
		Plugins []directoryEntry `json:"plugins"`
	}
	if err := decodeStrict(b, &dir); err != nil {
		return fmt.Errorf("directory: %w", err)
	}
	if dir.Plugins == nil { // publishing no plugins revokes them all, so it must be spelled out
		return errors.New(`directory: no "plugins" array; an empty list is written []`)
	}
	type outPlugin struct {
		Name      string            `json:"name"`
		Title     json.RawMessage   `json:"title"`
		Summary   json.RawMessage   `json:"summary"`
		Category  string            `json:"category"`
		Publisher string            `json:"publisher"`
		DataFlow  string            `json:"dataFlow"`
		Homepage  string            `json:"homepage"`
		Releases  []json.RawMessage `json:"releases"`
	}
	out := make([]outPlugin, 0, len(dir.Plugins))
	var errs []error
	for _, e := range dir.Plugins {
		if err := e.check(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name, err))
			continue
		}
		releases, err := e.releases()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name, err))
			continue
		}
		out = append(out, outPlugin{e.Name, e.Title, e.Summary, e.Category, e.Publisher, e.DataFlow, e.Homepage, releases})
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ") // the pull request's check diffs it against the published one
	if err := enc.Encode(map[string]any{"schema": 1, "updated": buildNow().UTC().Format(time.RFC3339), "plugins": out}); err != nil {
		return err
	}
	if _, err := plugins.CheckRegistry(buf.Bytes()); err != nil {
		return err
	}
	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}

func (e directoryEntry) check() error {
	for _, t := range []struct {
		what string
		raw  json.RawMessage
	}{{"title", e.Title}, {"summary", e.Summary}} {
		var text plugins.Text
		if len(t.raw) == 0 || json.Unmarshal(t.raw, &text) != nil || strings.TrimSpace(text.Default) == "" {
			return fmt.Errorf("%s needs a default text", t.what)
		}
	}
	switch {
	case !slices.Contains(categories, e.Category):
		return fmt.Errorf("category %q is not one of %v", e.Category, categories)
	case strings.TrimSpace(e.Publisher) == "":
		return errors.New("no publisher")
	case !slices.Contains(dataFlows, e.DataFlow):
		return fmt.Errorf("dataFlow %q is not one of %v", e.DataFlow, dataFlows)
	case !strings.HasPrefix(e.Homepage, "https://"):
		return errors.New("homepage must be https")
	case !repoRE.MatchString(e.Source.Repo) || !commitRE.MatchString(e.Source.SHA) ||
		e.Source.Path == "" || path.Clean(e.Source.Path) != e.Source.Path || path.IsAbs(e.Source.Path) || strings.HasPrefix(e.Source.Path, ".."):
		return errNoSource
	}
	return nil
}

// releases reads the entry's godwit-plugin.json at its commit and drops the
// revoked versions; each kept release stays exactly as the author wrote it.
func (e directoryEntry) releases() ([]json.RawMessage, error) {
	b, err := fetchSource(sourceBase + "/" + e.Source.Repo + "/" + e.Source.SHA + "/" + e.Source.Path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Name     string            `json:"name"`
		Releases []json.RawMessage `json:"releases"`
	}
	if err := decodeStrict(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", e.Source.Path, err)
	}
	if doc.Name != e.Name {
		return nil, fmt.Errorf("%s names plugin %q", e.Source.Path, doc.Name)
	}
	var kept []json.RawMessage
	unknown := slices.Clone(e.Revoked) // a mistyped version must not look revoked
	for _, r := range doc.Releases {
		var head struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(r, &head); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Source.Path, err)
		}
		if slices.Contains(e.Revoked, head.Version) {
			unknown = slices.DeleteFunc(unknown, func(v string) bool { return v == head.Version })
			continue
		}
		kept = append(kept, r)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("revoked versions %v are not released", unknown)
	}
	if len(kept) == 0 {
		return nil, errors.New("no releases left: remove the entry instead")
	}
	return kept, nil
}

// decodeStrict reads exactly one document with only known fields, as
// plugins.CheckRegistry does: what it dropped, no later check could see.
func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after the document")
	}
	return nil
}

func fetchSource(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchStall)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPluginFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPluginFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", url, maxPluginFile)
	}
	return b, nil
}

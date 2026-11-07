package core

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/core/plugins"
)

const maxPluginReadme = 256 << 10

var readmeLangRE = regexp.MustCompile(`^[A-Za-z]+(-[A-Za-z0-9]+)*$`)

// PluginReadme is a directory plugin's README for lang, read from GitHub when
// the user opens its install dialog (ADR 0032): README.<lang>.md, then
// README.md, at the branch's head. "" when its homepage is not on GitHub. It
// is not reviewed and not pinned, so it is only ever shown as text, and the
// reviewers' words in the directory stay what states the risk.
func (s *Service) PluginReadme(ctx context.Context, name, lang string) (string, error) {
	src := s.opts.Registry
	if src == nil {
		return "", ErrRegistryOff
	}
	s.registryMu.Lock()
	reg, err := s.cachedRegistry(src)
	s.registryMu.Unlock()
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(reg.Plugins, func(p plugins.RegistryPlugin) bool { return p.Name == name })
	if i < 0 {
		return "", nil
	}
	base := readmeBase(reg.Plugins[i].Homepage)
	if base == "" {
		return "", nil
	}
	var names []string
	if readmeLangRE.MatchString(lang) {
		names = append(names, "README."+lang+".md")
	}
	for _, n := range append(names, "README.md") {
		var b []byte
		if b, err = fetchRegistryFile(ctx, src, base+"/"+n, maxPluginReadme, nil); err == nil {
			return string(b), nil
		}
	}
	return "", err
}

// readmeBase is where a GitHub homepage's files are served: its repository's
// root, or the folder of a /tree/<branch>/<folder> link. "" for anything else.
func readmeBase(homepage string) string {
	u, err := url.Parse(homepage)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || slices.Contains(parts, "..") {
		return ""
	}
	raw := "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1]
	switch {
	case len(parts) == 2:
		return raw + "/HEAD"
	case parts[2] == "tree" && len(parts) >= 4:
		return raw + "/" + strings.Join(parts[3:], "/")
	}
	return ""
}

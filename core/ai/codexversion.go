package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// codexFallbackVersion is the Codex CLI release the model list is asked for
// when no lookup has succeeded yet.
const codexFallbackVersion = "0.159.0"

// codexVersionURLs name the latest Codex CLI release: GitHub's release, then
// npm's package. The first that answers wins. Tests point them at a fake.
var codexVersionURLs = []string{
	"https://api.github.com/repos/openai/codex/releases/latest",
	"https://registry.npmjs.org/@openai/codex/latest",
}

const (
	codexVersionFresh = time.Hour       // how long a looked-up version is used
	codexVersionRetry = 5 * time.Minute // how long a failed lookup is not repeated
)

var codexVersion struct {
	sync.Mutex
	version string
	until   time.Time
}

var stableVersion = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)

// codexClientVersion is the latest Codex CLI release, as its backend gates
// each model on the client version asking (minimal_client_version). A lookup
// is cached for an hour; after a failed one the last known version, or
// codexFallbackVersion, stands for five minutes.
func codexClientVersion(ctx context.Context, c *http.Client) string {
	codexVersion.Lock()
	defer codexVersion.Unlock()
	if codexVersion.version != "" && time.Now().Before(codexVersion.until) {
		return codexVersion.version
	}
	for _, u := range codexVersionURLs {
		if v := fetchCodexVersion(ctx, c, u); v != "" {
			codexVersion.version, codexVersion.until = v, time.Now().Add(codexVersionFresh)
			return v
		}
	}
	if codexVersion.version == "" {
		codexVersion.version = codexFallbackVersion
	}
	codexVersion.until = time.Now().Add(codexVersionRetry)
	return codexVersion.version
}

// fetchCodexVersion reads a stable release version from u, or "".
func fetchCodexVersion(ctx context.Context, c *http.Client, u string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	if c == nil {
		c = NoRedirectClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var body struct {
		TagName    string `json:"tag_name"` // GitHub: "rust-v0.159.0"
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
		Name       string `json:"name"` // npm: "@openai/codex"
		Version    string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil || body.Prerelease || body.Draft {
		return ""
	}
	v := body.Version
	if body.TagName != "" {
		v = strings.TrimPrefix(body.TagName, "rust-v")
	} else if body.Name != "@openai/codex" {
		return ""
	}
	if !stableVersion.MatchString(v) {
		return ""
	}
	return v
}

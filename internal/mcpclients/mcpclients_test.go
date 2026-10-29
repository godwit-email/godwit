package mcpclients

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var srv = Server{Command: "/Applications/Godwit.app/Contents/MacOS/Godwit", Args: []string{"mcp"}}

func env(t *testing.T) Env {
	return Env{Home: t.TempDir(), OS: "darwin", Run: func(string, ...string) error { t.Fatal("ran a command"); return nil }}
}

func write(t *testing.T, path, text string) {
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
}

func read(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func state(t *testing.T, e Env, app string) State {
	st, err := e.State(app, srv)
	require.NoError(t, err)
	return st
}

// Claude's config keeps the user's other servers, settings and the fields
// they gave godwit's entry; godwit's goes and comes back alone.
func TestClaudeKeepsTheRest(t *testing.T) {
	e := env(t)
	assert.Equal(t, Absent, state(t, e, Claude))
	path := filepath.Join(e.Home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	write(t, path, `{"globalShortcut": "Alt+Space", "mcpServers": {"files": {"command": "npx", "args": ["fs"]}, "godwit": {"command": "/old/Godwit", "env": {"X": "1"}}}}`)
	assert.Equal(t, Other, state(t, e, Claude), "godwit points at another program")

	require.NoError(t, e.Connect(Claude, srv))
	assert.Equal(t, On, state(t, e, Claude))
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(read(t, path)), &got))
	assert.Equal(t, "Alt+Space", got["globalShortcut"])
	servers := got["mcpServers"].(map[string]any)
	assert.Equal(t, map[string]any{"command": "npx", "args": []any{"fs"}}, servers["files"])
	assert.Equal(t, map[string]any{"command": srv.Command, "args": []any{"mcp"}, "env": map[string]any{"X": "1"}}, servers["godwit"])

	require.NoError(t, e.Disconnect(Claude))
	assert.Equal(t, Off, state(t, e, Claude))
	require.NoError(t, json.Unmarshal([]byte(read(t, path)), &got))
	assert.NotContains(t, got["mcpServers"], "godwit")
	assert.Contains(t, got["mcpServers"], "files")
}

// A config that is not JSON is left as it was.
func TestClaudeLeavesBrokenJSON(t *testing.T) {
	e := env(t)
	path := filepath.Join(e.Home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	write(t, path, `{"mcpServers": {`)
	assert.Error(t, e.Connect(Claude, srv))
	assert.Equal(t, `{"mcpServers": {`, read(t, path))
}

// The app's folder there but no config yet: Connect makes one.
func TestClaudeWithoutConfig(t *testing.T) {
	e := env(t)
	require.NoError(t, os.MkdirAll(filepath.Join(e.Home, "Library", "Application Support", "Claude"), 0o755))
	assert.Equal(t, Off, state(t, e, Claude))
	require.NoError(t, e.Connect(Claude, srv))
	assert.Equal(t, On, state(t, e, Claude))
}

// A config that is a link (a dotfiles folder's) stays a link, and keeps its mode.
func TestWriteKeepsALink(t *testing.T) {
	e := env(t)
	real := filepath.Join(e.Home, "dotfiles", "claude.json")
	write(t, real, `{}`)
	dir := filepath.Join(e.Home, "Library", "Application Support", "Claude")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	link := filepath.Join(dir, "claude_desktop_config.json")
	require.NoError(t, os.Symlink(real, link))
	require.NoError(t, e.Connect(Claude, srv))
	fi, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink, "still a link")
	assert.Contains(t, read(t, real), "godwit")
	fi, err = os.Stat(real)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

// Claude Code is changed through its CLI; its state is read from ~/.claude.json.
func TestClaudeCodeUsesItsCLI(t *testing.T) {
	e := env(t)
	assert.Equal(t, Absent, state(t, e, ClaudeCode))
	cli := filepath.Join(e.Home, ".local", "bin", "claude")
	write(t, cli, "")
	var ran []string
	e.Run = func(name string, args ...string) error {
		ran = append(ran, filepath.Base(name)+" "+strings.Join(args, " "))
		return nil
	}
	assert.Equal(t, Off, state(t, e, ClaudeCode))
	require.NoError(t, e.Connect(ClaudeCode, srv))
	require.NoError(t, e.Disconnect(ClaudeCode))
	assert.Equal(t, []string{
		"claude mcp remove --scope user godwit",
		`claude mcp add-json --scope user godwit {"args":["mcp"],"command":"/Applications/Godwit.app/Contents/MacOS/Godwit","type":"stdio"}`,
		"claude mcp remove --scope user godwit",
	}, ran)
	write(t, filepath.Join(e.Home, ".claude.json"), `{"projects": {}, "mcpServers": {"godwit": {"type": "stdio", "command": "/Applications/Godwit.app/Contents/MacOS/Godwit", "args": ["mcp"]}}}`)
	assert.Equal(t, On, state(t, e, ClaudeCode))
}

// Codex's config.toml is edited as text: comments, other tables and the
// keys and subtables the user gave godwit's table stay.
func TestCodexKeepsTheRest(t *testing.T) {
	e := env(t)
	assert.Equal(t, Absent, state(t, e, Codex))
	path := filepath.Join(e.Home, ".codex", "config.toml")
	write(t, path, `# my settings
model = "gpt-5"

[mcp_servers.godwit]
command = "/old/Godwit"
args = ["mcp"]
startup_timeout_sec = 20

[mcp_servers.godwit.env]
X = "1"

[mcp_servers.files]
command = "npx"
`)
	assert.Equal(t, Other, state(t, e, Codex))
	require.NoError(t, e.Connect(Codex, srv))
	assert.Equal(t, On, state(t, e, Codex))
	assert.Equal(t, `# my settings
model = "gpt-5"

[mcp_servers.godwit]
command = "/Applications/Godwit.app/Contents/MacOS/Godwit"
args = ["mcp"]
startup_timeout_sec = 20

[mcp_servers.godwit.env]
X = "1"

[mcp_servers.files]
command = "npx"
`, read(t, path))

	require.NoError(t, e.Disconnect(Codex))
	assert.Equal(t, Off, state(t, e, Codex))
	assert.Equal(t, `# my settings
model = "gpt-5"

[mcp_servers.files]
command = "npx"
`, read(t, path))

	require.NoError(t, e.Connect(Codex, srv)) // a new table goes at the end
	assert.Equal(t, `# my settings
model = "gpt-5"

[mcp_servers.files]
command = "npx"

[mcp_servers.godwit]
command = "/Applications/Godwit.app/Contents/MacOS/Godwit"
args = ["mcp"]
`, read(t, path))
}

// $CODEX_HOME is where Codex is, when set.
func TestCodexHome(t *testing.T) {
	e := env(t)
	e.CodexHome = filepath.Join(e.Home, "elsewhere")
	require.NoError(t, os.MkdirAll(e.CodexHome, 0o755))
	require.NoError(t, e.Connect(Codex, srv))
	assert.Contains(t, read(t, filepath.Join(e.CodexHome, "config.toml")), "[mcp_servers.godwit]")
}

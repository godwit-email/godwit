// Package mcpclients connects godwit mcp to the AI apps that read MCP
// servers from a config file of their own: Claude (the desktop app), Claude
// Code and Codex. It changes only the server named godwit in each, and
// leaves everything else in those files as it was.
package mcpclients

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Name is the server's name in each app's config.
const Name = "godwit"

// The apps, by ID.
const (
	Claude     = "claude"      // the desktop app: claude_desktop_config.json
	ClaudeCode = "claude-code" // through its own CLI, claude mcp
	Codex      = "codex"       // ~/.codex/config.toml
)

// IDs are the apps in the order they are offered.
var IDs = []string{Claude, ClaudeCode, Codex}

// Server is how an app runs godwit mcp.
type Server struct {
	Command string
	Args    []string
}

// State is how an app stands with godwit mcp.
type State int

const (
	Absent State = iota // not on this computer
	Off                 // there, without a godwit server
	On                  // its godwit server runs this Server
	Other               // its godwit server runs something else: another Godwit, or one since moved
)

// Env is where the apps are looked for; tests point it at a folder.
type Env struct {
	Home string
	OS   string // runtime.GOOS
	// AppData is %APPDATA% on Windows.
	AppData string
	// CodexHome is $CODEX_HOME, if set.
	CodexHome string
	// Run runs Claude Code's CLI; nil runs it.
	Run func(name string, args ...string) error
}

// Local is this computer's Env.
func Local() (Env, error) {
	home, err := os.UserHomeDir()
	return Env{Home: home, OS: runtime.GOOS, AppData: os.Getenv("APPDATA"), CodexHome: os.Getenv("CODEX_HOME")}, err
}

// State says how app stands with s.
func (e Env) State(app string, s Server) (State, error) {
	switch app {
	case Claude, ClaudeCode:
		path, ok := e.jsonFile(app)
		if !ok {
			return Absent, nil
		}
		entry, err := readJSONServer(path)
		if err != nil || entry == nil {
			return Off, err
		}
		if same(entry.Command, entry.Args, s) {
			return On, nil
		}
		return Other, nil
	case Codex:
		dir := e.codexDir()
		if !isDir(dir) {
			return Absent, nil
		}
		b, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if errors.Is(err, os.ErrNotExist) {
			return Off, nil
		}
		if err != nil {
			return Off, err
		}
		cmd, args, found := tomlServer(string(b))
		switch {
		case codexElsewhere(strings.Split(string(b), "\n")):
			return Other, nil
		case !found:
			return Off, nil
		case same(cmd, args, s):
			return On, nil
		}
		return Other, nil
	}
	return Absent, fmt.Errorf("unknown app %q", app)
}

// Connect gives app a godwit server that runs s, in place of any it had.
// The server's other fields there (an env the user gave it) are kept.
func (e Env) Connect(app string, s Server) error {
	switch app {
	case Claude:
		path, ok := e.jsonFile(app)
		if !ok {
			return errors.New("Claude is not on this computer")
		}
		return editJSONServer(path, &s)
	case ClaudeCode:
		cli, ok := e.claudeCLI()
		if !ok {
			return errors.New("Claude Code is not on this computer")
		}
		entry, _ := json.Marshal(map[string]any{"type": "stdio", "command": s.Command, "args": s.Args})
		_ = e.run(cli, "mcp", "remove", "--scope", "user", Name) // none yet is fine
		return e.run(cli, "mcp", "add-json", "--scope", "user", Name, string(entry))
	case Codex:
		return e.editCodex(&s)
	}
	return fmt.Errorf("unknown app %q", app)
}

// Disconnect takes app's godwit server away.
func (e Env) Disconnect(app string) error {
	switch app {
	case Claude:
		path, ok := e.jsonFile(app)
		if !ok {
			return nil
		}
		return editJSONServer(path, nil)
	case ClaudeCode:
		cli, ok := e.claudeCLI()
		if !ok {
			return nil
		}
		return e.run(cli, "mcp", "remove", "--scope", "user", Name)
	case Codex:
		return e.editCodex(nil)
	}
	return fmt.Errorf("unknown app %q", app)
}

func same(cmd string, args []string, s Server) bool {
	return cmd == s.Command && slices.Equal(args, s.Args)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// jsonFile is the JSON file app keeps its MCP servers in, and whether app
// is on this computer: Claude's folder, or Claude Code's CLI.
func (e Env) jsonFile(app string) (string, bool) {
	if app == ClaudeCode {
		_, ok := e.claudeCLI()
		return filepath.Join(e.Home, ".claude.json"), ok
	}
	var dir string
	switch e.OS {
	case "darwin":
		dir = filepath.Join(e.Home, "Library", "Application Support", "Claude")
	case "windows":
		dir = filepath.Join(e.AppData, "Claude")
	default:
		dir = filepath.Join(e.Home, ".config", "Claude")
	}
	return filepath.Join(dir, "claude_desktop_config.json"), isDir(dir)
}

// claudeCLI finds Claude Code's claude: where its installers put it, as a
// GUI app's PATH holds none of these.
func (e Env) claudeCLI() (string, bool) {
	exe := "claude"
	if e.OS == "windows" {
		exe = "claude.exe"
	}
	places := []string{filepath.Join(e.Home, ".local", "bin", exe), filepath.Join(e.Home, ".claude", "local", exe)}
	if e.Run == nil { // this computer's, not a test's folder
		places = append(places, filepath.Join("/opt/homebrew/bin", exe), filepath.Join("/usr/local/bin", exe))
		if p, err := exec.LookPath(exe); err == nil {
			places = append(places, p)
		}
	}
	for _, p := range places {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	return "", false
}

func (e Env) run(name string, args ...string) error {
	if e.Run != nil {
		return e.Run(name, args...)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (e Env) codexDir() string {
	if e.CodexHome != "" {
		return e.CodexHome
	}
	return filepath.Join(e.Home, ".codex")
}

type jsonServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// readJSONServer is the godwit entry of path's mcpServers; nil if none.
func readJSONServer(path string) (*jsonServer, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var root struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	raw, ok := root.MCPServers[Name]
	if !ok {
		return nil, nil
	}
	var s jsonServer
	return &s, json.Unmarshal(raw, &s)
}

// editJSONServer sets path's godwit server to s, or takes it away if s is
// nil. A file that is not a JSON object is left alone: an error says so.
func editJSONServer(path string, s *Server) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var root map[string]json.RawMessage
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &root); err != nil {
			return fmt.Errorf("%s is not valid JSON, so Godwit left it alone: %w", path, err)
		}
	}
	if root == nil { // an empty file, or null
		root = map[string]json.RawMessage{}
	}
	var servers map[string]json.RawMessage
	if raw, ok := root["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("%s: mcpServers is not an object, so Godwit left it alone: %w", path, err)
		}
	}
	if servers == nil { // none, or null
		servers = map[string]json.RawMessage{}
	}
	if s == nil {
		if _, ok := servers[Name]; !ok {
			return nil
		}
		delete(servers, Name)
	} else {
		var entry map[string]json.RawMessage
		if raw, ok := servers[Name]; ok {
			_ = json.Unmarshal(raw, &entry) // the user's own fields stay
		}
		if entry == nil { // none, null, or not an object
			entry = map[string]json.RawMessage{}
		}
		entry["command"], _ = json.Marshal(s.Command)
		entry["args"], _ = json.Marshal(s.Args)
		servers[Name], _ = json.Marshal(entry)
	}
	root["mcpServers"], _ = json.Marshal(servers)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(out, '\n'))
}

// writeFile replaces path's content at once, through a link to where it
// points (a dotfiles folder's), keeping its mode.
func writeFile(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".godwit-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path) // the app sees the old file or the new one, never half of one
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Codex keeps its servers as tables of config.toml, [mcp_servers.<name>],
// with subtables such as [mcp_servers.<name>.env]. The file is edited as
// text, so its comments and order stay: a header is read as TOML spells it
// (quoted keys, spaces around dots, a comment after it), and a key with its
// whole value, which may run over several lines.

const codexHeader = "[mcp_servers." + Name + "]"

// tomlHeader reads a table header line, [a.b] or [[a.b]], a comment after
// it allowed: its key, unquoted part by part; false for any other line.
func tomlHeader(line string) (key []string, ok bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") {
		return nil, false
	}
	open, shut := "[", "]"
	if strings.HasPrefix(t, "[[") {
		open, shut = "[[", "]]"
	}
	body := t[len(open):]
	var q byte
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case q == '"' && c == '\\':
			i++
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case strings.HasPrefix(body[i:], shut):
			if rest := strings.TrimSpace(body[i+len(shut):]); rest != "" && !strings.HasPrefix(rest, "#") {
				return nil, false
			}
			return tomlKey(body[:i]), true
		}
	}
	return nil, false
}

// tomlKey splits a dotted key into its parts, each unquoted.
func tomlKey(k string) []string {
	var parts []string
	var cur strings.Builder
	var q byte
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case q == '"' && c == '\\' && i+1 < len(k):
			i++
			cur.WriteByte(k[i])
		case q != 0:
			if c == q {
				q = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			q = c
		case c == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		case c != ' ' && c != '\t':
			cur.WriteByte(c)
		}
	}
	return append(parts, strings.TrimSpace(cur.String()))
}

// godwitTable is whether key names godwit's table (0), one of its
// subtables (1), or neither (-1).
func godwitTable(key []string) int {
	if len(key) < 2 || key[0] != "mcp_servers" || key[1] != Name {
		return -1
	}
	if len(key) == 2 {
		return 0
	}
	return 1
}

// codexSpan is where godwit's table is in lines: its header, the end of its
// own keys (before a subtable of it), and its end with its subtables; -1 if
// none. n is how many godwit tables there are: more than one is a file
// Codex can't read either, and is left alone.
func codexSpan(lines []string) (start, keys, end, n int) {
	start, keys, end = -1, len(lines), len(lines)
	for i, l := range lines {
		key, ok := tomlHeader(l)
		if !ok || strings.HasPrefix(strings.TrimSpace(l), "[[") {
			if ok && start >= 0 && end == len(lines) {
				end = i
			}
			continue
		}
		switch godwitTable(key) {
		case 0:
			n++
			if start < 0 {
				start = i
				continue
			}
		case 1:
			if start >= 0 && end == len(lines) {
				keys = min(keys, i)
			}
			continue
		}
		if start >= 0 && end == len(lines) {
			end = i
		}
	}
	if start < 0 {
		return -1, -1, -1, 0
	}
	return start, min(keys, end), end, n // the blank lines before the next table go with it
}

// codexElsewhere is whether lines define godwit's server, or mcp_servers
// whole, other than in godwit's own tables: with dotted keys
// (mcp_servers.godwit.command = …) or an inline table (godwit = {…} under
// [mcp_servers]). Godwit can't add a table there without breaking the file.
func codexElsewhere(lines []string) bool {
	var table []string
	for i := 0; i < len(lines); {
		if key, ok := tomlHeader(lines[i]); ok {
			table = key
			i++
			continue
		}
		k, _, n := tomlEntry(lines, i)
		i += n
		if k == "" || godwitTable(table) >= 0 {
			continue
		}
		raw, _, _ := strings.Cut(lines[i-n], "=")
		full := append(slices.Clone(table), tomlKey(raw)...)
		godwit := []string{"mcp_servers", Name}
		m := min(len(full), len(godwit))
		if slices.Equal(full[:m], godwit[:m]) {
			return true
		}
	}
	return false
}

// tomlEntry reads lines[i] as key = value: the key (unquoted), the value's
// text, and how many lines key and value span (1 for a line that is no
// key: a comment, a blank).
func tomlEntry(lines []string, i int) (key, value string, span int) {
	k, v, ok := strings.Cut(lines[i], "=")
	if !ok || strings.HasPrefix(strings.TrimSpace(k), "#") {
		return "", "", 1
	}
	key = strings.Join(tomlKey(k), ".")
	text := v
	span = 1
	for !tomlValueEnds(text) && i+span < len(lines) {
		text += "\n" + lines[i+span]
		span++
	}
	return key, strings.TrimSpace(text), span
}

// tomlValueEnds is whether v is a whole value: no string, array or inline
// table left open (a comment after it allowed).
func tomlValueEnds(v string) bool {
	depth := 0
	var q string // the open string's delimiter
	for i := 0; i < len(v); i++ {
		switch {
		case q != "":
			if q == `"` || q == `"""` {
				if v[i] == '\\' {
					i++
					continue
				}
			}
			if strings.HasPrefix(v[i:], q) {
				i += len(q) - 1
				q = ""
			}
		case strings.HasPrefix(v[i:], `"""`), strings.HasPrefix(v[i:], "'''"):
			q = v[i : i+3]
			i += 2
		case v[i] == '"' || v[i] == '\'':
			q = string(v[i])
		case v[i] == '#':
			for i < len(v) && v[i] != '\n' { // to the line's end
				i++
			}
		case v[i] == '[' || v[i] == '{':
			depth++
		case v[i] == ']' || v[i] == '}':
			depth--
		}
	}
	return q == "" && depth <= 0
}

// tomlServer reads godwit's command and args from a Codex config.
func tomlServer(text string) (cmd string, args []string, found bool) {
	lines := strings.Split(text, "\n")
	start, keys, _, _ := codexSpan(lines)
	if start < 0 {
		return "", nil, false
	}
	for i := start + 1; i < keys; {
		k, v, n := tomlEntry(lines, i)
		switch k {
		case "command":
			cmd = tomlString(v)
		case "args":
			_ = json.Unmarshal([]byte(v), &args) // a one-line array of basic strings, as Godwit writes it; else another Godwit's
		}
		i += n
	}
	return cmd, args, true
}

// tomlString reads a basic ("…") or literal ('…') string.
func tomlString(v string) string {
	if strings.HasPrefix(v, "'") {
		return strings.Trim(v, "'")
	}
	var s string
	_ = json.Unmarshal([]byte(v), &s) // TOML's basic strings escape as JSON's do
	return s
}

// editCodex sets godwit's table in Codex's config.toml to s, keeping any
// other keys and subtables the user gave it, or takes it away if s is nil.
// What it would write must hold one godwit table, or none: else the file
// is left alone.
func (e Env) editCodex(s *Server) error {
	dir := e.codexDir()
	if s != nil && !isDir(dir) {
		return errors.New("Codex is not on this computer")
	}
	path := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(string(b), "\n")
	start, keys, end, n := codexSpan(lines)
	if n > 1 {
		return fmt.Errorf("%s has %d [mcp_servers.%s] tables, so Godwit left it alone", path, n, Name)
	}
	if codexElsewhere(lines) {
		return fmt.Errorf("%s sets mcp_servers.%s outside its own table, so Godwit left it alone", path, Name)
	}
	var out []string
	switch {
	case s == nil && start < 0:
		return nil
	case s == nil:
		out = append(slices.Clone(lines[:start]), lines[end:]...)
	default:
		cmd, _ := json.Marshal(s.Command)
		args, _ := json.Marshal(s.Args)
		ours := []string{"command = " + string(cmd), "args = " + string(args)}
		if start < 0 {
			text := strings.TrimRight(string(b), "\n")
			if text != "" {
				text += "\n\n"
			}
			out = strings.Split(text+codexHeader+"\n"+strings.Join(ours, "\n")+"\n", "\n")
			break
		}
		var kept []string // the user's own keys in the table, each with all its lines
		for i := start + 1; i < keys; {
			k, _, n := tomlEntry(lines, i)
			if k != "command" && k != "args" {
				kept = append(kept, lines[i:i+n]...)
			}
			i += n
		}
		out = slices.Clone(lines[:start+1])
		out = append(out, ours...)
		out = append(out, kept...)
		out = append(out, lines[keys:]...)
	}
	want := 1
	if s == nil {
		want = 0
	}
	if _, _, _, n := codexSpan(out); n != want {
		return fmt.Errorf("%s: Godwit could not edit its [mcp_servers.%s] table safely, so it left the file alone", path, Name)
	}
	return writeFile(path, []byte(strings.Join(out, "\n")))
}

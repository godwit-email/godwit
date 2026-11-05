package core

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
)

// Antigravity is the interface for Google Antigravity ACP OAuth, model discovery, and inference.
type Antigravity interface {
	StartOAuth(ctx context.Context) (flow, error)
	ListModels(ctx context.Context, tokenJSON string) ([]ai.Model, error)
	Generate(ctx context.Context, tokenJSON, model, prompt string, onChunk func(string)) (string, error)
	Judge(ctx context.Context, tokenJSON, model, input string, questions []ai.Question) ([]ai.Answer, error)
}

type antigravityBinarySpec struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type antigravityReleaseAsset struct {
	AgentID      string                `json:"agentId"`
	Version      string                `json:"version"`
	PlatformKey  string                `json:"platformKey"`
	ArchiveURL   string                `json:"archiveUrl"`
	ArchiveBytes int64                 `json:"archiveBytes"`
	SHA256       string                `json:"sha256"`
	Executable   antigravityBinarySpec `json:"executable"`
	Harness      antigravityBinarySpec `json:"harness"`
}

type antigravityInstallReceipt struct {
	AgentID     string `json:"agentId"`
	Version     string `json:"version"`
	PlatformKey string `json:"platformKey"`
	ArchiveURL  string `json:"archiveUrl"`
	SHA256      string `json:"sha256"`
	Executable  string `json:"executable"`
	Harness     string `json:"harness"`
}

type antigravityRuntime struct {
	ExecutablePath string
	HarnessPath    string
	InstallDir     string
	Version        string
}

const (
	antigravityCurrentVersion = "1.4.5"
	antigravityReceiptFile    = ".install-complete.json"
	antigravityBrowserMarker  = "__GODWIT_ANTIGRAVITY_AUTH_URL__"
	antigravityAuthPrefix     = "Open the following link to authenticate the ACP server: "
)

var antigravityAssetsV145 = map[string]antigravityReleaseAsset{
	"darwin-arm64": {
		AgentID:      "antigravity",
		Version:      antigravityCurrentVersion,
		PlatformKey:  "darwin-arm64",
		ArchiveURL:   "https://dl.google.com/agy-extensions/releases/macos/agy-acp-server-1.4.5-darwin-arm64.zip",
		ArchiveBytes: 97_923_855,
		SHA256:       "886fff7767d08994ca39eb02c3d01a7532c7325a94aa33aaf52d52068733284f",
		Executable:   antigravityBinarySpec{Name: "agy_acp_server.par", Bytes: 178_915_920},
		Harness:      antigravityBinarySpec{Name: "localharness_external", Bytes: 122_957_936},
	},
	"darwin-x64": {
		AgentID:      "antigravity",
		Version:      antigravityCurrentVersion,
		PlatformKey:  "darwin-x64",
		ArchiveURL:   "https://dl.google.com/agy-extensions/releases/macos/agy-acp-server-1.4.5-darwin-x86_64.zip",
		ArchiveBytes: 103_577_295,
		SHA256:       "b39f3573c03b9acb9df028853f647527f1f3133f32bd77e78b796e48a205114a",
		Executable:   antigravityBinarySpec{Name: "agy_acp_server.par", Bytes: 183_127_600},
		Harness:      antigravityBinarySpec{Name: "localharness_external", Bytes: 127_612_208},
	},
	"linux-arm64": {
		AgentID:      "antigravity",
		Version:      antigravityCurrentVersion,
		PlatformKey:  "linux-arm64",
		ArchiveURL:   "https://dl.google.com/agy-extensions/releases/linux/agy-acp-server-1.4.5-linux-arm64.zip",
		ArchiveBytes: 307_829_008,
		SHA256:       "7d64246633ae094f67e3bbcd2d679e89048f91346d5339313336e7483f40f056",
		Executable:   antigravityBinarySpec{Name: "agy_acp_server.par", Bytes: 831_194_025},
		Harness:      antigravityBinarySpec{Name: "localharness_external", Bytes: 127_512_832},
	},
	"linux-x64": {
		AgentID:      "antigravity",
		Version:      antigravityCurrentVersion,
		PlatformKey:  "linux-x64",
		ArchiveURL:   "https://dl.google.com/agy-extensions/releases/linux/agy-acp-server-1.4.5-linux-x86_64.zip",
		ArchiveBytes: 321_783_181,
		SHA256:       "923923a0d7dc8073f66cc6f78647a3107e3a7ee2b7e1a24df8b3927b5188013b",
		Executable:   antigravityBinarySpec{Name: "agy_acp_server.par", Bytes: 826_226_766},
		Harness:      antigravityBinarySpec{Name: "localharness_external", Bytes: 133_926_136},
	},
	"win32-arm64": {
		AgentID:      "antigravity",
		Version:      antigravityCurrentVersion,
		PlatformKey:  "win32-arm64",
		ArchiveURL:   "https://dl.google.com/agy-extensions/releases/windows/agy-acp-server-1.4.5-win32-arm64.zip",
		ArchiveBytes: 102_703_121,
		SHA256:       "ab0a9aac8349e08ef289be6d16da8f89e3abe60eba5158032ef78c4a1220261c",
		Executable:   antigravityBinarySpec{Name: "agy_acp_server.exe", Bytes: 62_293_488},
		Harness:      antigravityBinarySpec{Name: "localharness_external.exe", Bytes: 139_322_008},
	},
	"win32-x64": {
		AgentID:      "antigravity",
		Version:      antigravityCurrentVersion,
		PlatformKey:  "win32-x64",
		ArchiveURL:   "https://dl.google.com/agy-extensions/releases/windows/agy-acp-server-1.4.5-win32-x64.zip",
		ArchiveBytes: 100_423_655,
		SHA256:       "d24f4c73c9f96d4f44384e281c855a56c72d8eaa9c5b0fad7ccb1635c6b63f50",
		Executable:   antigravityBinarySpec{Name: "agy_acp_server.exe", Bytes: 55_839_864},
		Harness:      antigravityBinarySpec{Name: "localharness_external.exe", Bytes: 147_843_736},
	},
}

func currentAntigravityPlatformKey() string {
	var plat string
	switch runtime.GOOS {
	case "darwin":
		plat = "darwin"
	case "linux":
		plat = "linux"
	case "windows":
		plat = "win32"
	default:
		return ""
	}
	var arch string
	switch runtime.GOARCH {
	case "arm64":
		arch = "arm64"
	case "amd64":
		arch = "x64"
	default:
		return ""
	}
	return plat + "-" + arch
}

func resolveAntigravityAsset() (antigravityReleaseAsset, bool) {
	asset, ok := antigravityAssetsV145[currentAntigravityPlatformKey()]
	return asset, ok
}

type realAntigravity struct {
	dataDir   string
	installMu sync.Mutex
}

func newRealAntigravity(dataDir string) *realAntigravity {
	return &realAntigravity{dataDir: dataDir}
}

func (r *realAntigravity) runtimeDir() string {
	return filepath.Join(r.dataDir, "runtimes", "antigravity")
}

func (r *realAntigravity) profileDirForToken(tokenJSON string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tokenJSON)))
	id := hex.EncodeToString(sum[:])[:24]
	return filepath.Join(r.dataDir, "providers", "antigravity", id)
}

func validateInstalledRuntime(installDir string, asset antigravityReleaseAsset) (antigravityRuntime, error) {
	execPath := filepath.Join(installDir, asset.Executable.Name)
	harnessPath := filepath.Join(installDir, asset.Harness.Name)
	receiptPath := filepath.Join(installDir, antigravityReceiptFile)

	if err := verifyBinaryFile(execPath, asset.Executable.Bytes); err != nil {
		return antigravityRuntime{}, err
	}
	if err := verifyBinaryFile(harnessPath, asset.Harness.Bytes); err != nil {
		return antigravityRuntime{}, err
	}
	b, err := os.ReadFile(receiptPath)
	if err != nil {
		return antigravityRuntime{}, err
	}
	var rec antigravityInstallReceipt
	if err := json.Unmarshal(b, &rec); err != nil {
		return antigravityRuntime{}, err
	}
	if rec.AgentID != asset.AgentID ||
		rec.Version != asset.Version ||
		rec.PlatformKey != asset.PlatformKey ||
		!strings.EqualFold(rec.SHA256, asset.SHA256) ||
		rec.Executable != asset.Executable.Name ||
		rec.Harness != asset.Harness.Name {
		return antigravityRuntime{}, errors.New("antigravity install receipt mismatch")
	}
	return antigravityRuntime{
		ExecutablePath: execPath,
		HarnessPath:    harnessPath,
		InstallDir:     installDir,
		Version:        asset.Version,
	}, nil
}

func verifyBinaryFile(path string, expectedBytes int64) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", path)
	}
	if fi.Size() <= 0 || (expectedBytes > 0 && fi.Size() != expectedBytes) {
		return fmt.Errorf("size mismatch for %s: got %d, want %d", filepath.Base(path), fi.Size(), expectedBytes)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("file not executable: %s", path)
	}
	return nil
}

func (r *realAntigravity) ensureRuntime(ctx context.Context) (antigravityRuntime, error) {
	asset, ok := resolveAntigravityAsset()
	if !ok {
		return antigravityRuntime{}, errors.New("Antigravity ACP does not support this platform/architecture")
	}
	installDir := r.runtimeDir()
	if rt, err := validateInstalledRuntime(installDir, asset); err == nil {
		return rt, nil
	}

	r.installMu.Lock()
	defer r.installMu.Unlock()

	if rt, err := validateInstalledRuntime(installDir, asset); err == nil {
		return rt, nil
	}

	parentDir := filepath.Dir(installDir)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return antigravityRuntime{}, fmt.Errorf("create runtime parent dir: %w", err)
	}

	stagingDir, err := os.MkdirTemp(parentDir, ".antigravity-staging-*")
	if err != nil {
		return antigravityRuntime{}, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	archivePath := filepath.Join(stagingDir, "agy-acp-server.zip")
	extractDir := filepath.Join(stagingDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return antigravityRuntime{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.ArchiveURL, nil)
	if err != nil {
		return antigravityRuntime{}, err
	}
	dlClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := dlClient.Do(req)
	if err != nil {
		return antigravityRuntime{}, fmt.Errorf("download Antigravity ACP runtime: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return antigravityRuntime{}, fmt.Errorf("download Antigravity ACP runtime: HTTP %d", resp.StatusCode)
	}

	outFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return antigravityRuntime{}, err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(outFile, hasher), resp.Body)
	closeErr := outFile.Close()
	if copyErr != nil {
		return antigravityRuntime{}, fmt.Errorf("download Antigravity ACP runtime: %w", copyErr)
	}
	if closeErr != nil {
		return antigravityRuntime{}, closeErr
	}
	if written != asset.ArchiveBytes {
		return antigravityRuntime{}, fmt.Errorf("Antigravity ACP archive size mismatch: got %d, want %d", written, asset.ArchiveBytes)
	}
	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualSHA, asset.SHA256) {
		return antigravityRuntime{}, fmt.Errorf("Antigravity ACP archive SHA-256 mismatch")
	}

	if err := extractAntigravityZip(archivePath, extractDir, asset); err != nil {
		return antigravityRuntime{}, err
	}

	receipt := antigravityInstallReceipt{
		AgentID:     asset.AgentID,
		Version:     asset.Version,
		PlatformKey: asset.PlatformKey,
		ArchiveURL:  asset.ArchiveURL,
		SHA256:      asset.SHA256,
		Executable:  asset.Executable.Name,
		Harness:     asset.Harness.Name,
	}
	recBytes, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return antigravityRuntime{}, err
	}
	if err := os.WriteFile(filepath.Join(extractDir, antigravityReceiptFile), recBytes, 0o600); err != nil {
		return antigravityRuntime{}, err
	}

	if _, err := validateInstalledRuntime(extractDir, asset); err != nil {
		return antigravityRuntime{}, err
	}

	_ = os.RemoveAll(installDir)
	if err := os.Rename(extractDir, installDir); err != nil {
		return antigravityRuntime{}, fmt.Errorf("install Antigravity ACP runtime: %w", err)
	}

	return validateInstalledRuntime(installDir, asset)
}

func extractAntigravityZip(archivePath, targetDir string, asset antigravityReleaseAsset) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open Antigravity archive: %w", err)
	}
	defer zr.Close()

	expected := map[string]int64{
		asset.Executable.Name: asset.Executable.Bytes,
		asset.Harness.Name:    asset.Harness.Bytes,
	}
	seen := map[string]bool{}

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		cleanName := filepath.Base(strings.ReplaceAll(f.Name, "\\", "/"))
		wantBytes, ok := expected[cleanName]
		if !ok || cleanName != f.Name {
			return fmt.Errorf("unexpected archive entry: %s", f.Name)
		}
		if seen[cleanName] {
			return fmt.Errorf("duplicate archive entry: %s", cleanName)
		}
		if int64(f.UncompressedSize64) != wantBytes {
			return fmt.Errorf("unexpected size for %s: got %d, want %d", cleanName, f.UncompressedSize64, wantBytes)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		outPath := filepath.Join(targetDir, cleanName)
		outFile, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(outFile, rc)
		rc.Close()
		closeErr := outFile.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		_ = os.Chmod(outPath, 0o755)
		seen[cleanName] = true
	}

	for name := range expected {
		if !seen[name] {
			return fmt.Errorf("missing binary in Antigravity archive: %s", name)
		}
	}
	return nil
}

var strippedAntigravityEnvKeys = map[string]bool{
	"AGY_AUTH_INFO":                       true,
	"AGY_AUTH_TOKEN":                      true,
	"ANTIGRAVITY_API_KEY":                 true,
	"ANTIGRAVITY_CLI_AUTH_TYPE":           true,
	"ANTIGRAVITY_REST_URL":                true,
	"CLOUD_SHELL":                         true,
	"GEMINI_API_KEY":                      true,
	"GEMINI_CLI_AUTH_TYPE":                true,
	"GEMINI_CLI_HOME":                     true,
	"GEMINI_FORCE_ENCRYPTED_FILE_STORAGE": true,
	"GEMINI_FORCE_FILE_STORAGE":           true,
	"GOOGLE_API_KEY":                      true,
	"GOOGLE_APPLICATION_CREDENTIALS":      true,
	"GOOGLE_CLOUD_ACCESS_TOKEN":           true,
	"GOOGLE_CLOUD_LOCATION":               true,
	"GOOGLE_CLOUD_PROJECT":                true,
	"GOOGLE_DEFAULT_CLIENT_ID":            true,
	"GOOGLE_DEFAULT_CLIENT_SECRET":        true,
	"GOOGLE_GENAI_USE_GCA":                true,
	"GOOGLE_GENAI_USE_VERTEXAI":           true,
	"GOOGLE_WORKSPACE_PROJECT_ID":         true,
	"NO_BROWSER":                          true,
	"OAUTH_CLIENT_ID":                     true,
	"OAUTH_CLIENT_SECRET":                 true,
}

func shouldStripAntigravityEnv(key string) bool {
	upper := strings.ToUpper(key)
	if strippedAntigravityEnvKeys[upper] {
		return true
	}
	for _, prefix := range []string{"AGY_", "ANTIGRAVITY_CLI_", "GEMINI_CLI_", "GOOGLE_OAUTH_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

func prepareAntigravityProfile(profileDir string, rt antigravityRuntime, tokenJSON string) ([]string, error) {
	settingsDir := filepath.Join(profileDir, "antigravity-acp")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(profileDir, 0o700)
	_ = os.Chmod(settingsDir, 0o700)

	settingsPath := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte("{\n  \"auth\": {\n    \"type\": \"oauth-personal\"\n  }\n}\n"), 0o600); err != nil {
		return nil, err
	}

	browserHelperPath := filepath.Join(profileDir, "capture-auth-url.sh")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%s%%s\\n' \"$1\" >&2\n", antigravityBrowserMarker)
	if err := os.WriteFile(browserHelperPath, []byte(script), 0o700); err != nil {
		return nil, err
	}

	if strings.TrimSpace(tokenJSON) != "" {
		tokenPath := filepath.Join(settingsDir, "acp_token.json")
		if err := os.WriteFile(tokenPath, []byte(tokenJSON), 0o600); err != nil {
			return nil, err
		}
	}

	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !shouldStripAntigravityEnv(k) {
			env = append(env, kv)
		}
	}
	env = append(env,
		"GEMINI_HOME="+profileDir,
		"AGY_ACP_FORCE_FILE_STORAGE=1",
		"ANTIGRAVITY_HARNESS_PATH="+rt.HarnessPath,
		"BROWSER="+browserHelperPath,
		"PYTHONUNBUFFERED=1",
		"ELECTRON_RUN_AS_NODE=1",
	)
	return env, nil
}

func extractAuthURLFromLine(line string) string {
	if _, after, ok := strings.Cut(line, antigravityBrowserMarker); ok {
		return strings.TrimSpace(after)
	}
	if _, after, ok := strings.Cut(line, antigravityAuthPrefix); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func validateAntigravityAuthURL(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "accounts.google.com") || u.Path != "/o/oauth2/v2/auth" {
		return "", errors.New("invalid Antigravity Google OAuth URL")
	}
	q := u.Query()
	redirectURI := strings.TrimSpace(q.Get("redirect_uri"))
	state := strings.TrimSpace(q.Get("state"))
	respType := strings.TrimSpace(q.Get("response_type"))
	clientID := strings.TrimSpace(q.Get("client_id"))
	if redirectURI == "" || state == "" || respType != "code" || clientID == "" {
		return "", errors.New("missing required OAuth parameters in Antigravity URL")
	}
	ru, err := url.Parse(redirectURI)
	if err != nil || ru.Scheme != "http" {
		return "", errors.New("invalid Antigravity OAuth redirect URI")
	}
	host := strings.ToLower(ru.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return "", errors.New("Antigravity OAuth redirect URI is not loopback")
		}
	}
	return u.String(), nil
}

func fetchAntigravityUserEmail(ctx context.Context, tokenBytes []byte) string {
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		TokenURI     string `json:"token_uri"`
	}
	if err := json.Unmarshal(tokenBytes, &tok); err != nil {
		return "google-account"
	}
	accessToken := strings.TrimSpace(tok.AccessToken)
	if accessToken == "" && tok.RefreshToken != "" && tok.ClientID != "" && tok.ClientSecret != "" {
		tokenURI := strings.TrimSpace(tok.TokenURI)
		if tokenURI == "" {
			tokenURI = "https://oauth2.googleapis.com/token"
		}
		form := url.Values{
			"client_id":     {tok.ClientID},
			"client_secret": {tok.ClientSecret},
			"refresh_token": {tok.RefreshToken},
			"grant_type":    {"refresh_token"},
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(rctx, http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if resp, err := http.DefaultClient.Do(req); err == nil {
				defer resp.Body.Close()
				var tr struct {
					AccessToken string `json:"access_token"`
				}
				if json.NewDecoder(resp.Body).Decode(&tr) == nil {
					accessToken = strings.TrimSpace(tr.AccessToken)
				}
			}
		}
	}
	if accessToken != "" {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+accessToken)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				defer resp.Body.Close()
				var ui struct {
					Email string `json:"email"`
				}
				if json.NewDecoder(resp.Body).Decode(&ui) == nil && strings.TrimSpace(ui.Email) != "" {
					return strings.TrimSpace(ui.Email)
				}
			}
		}
	}
	return "google-account"
}

type antigravityOAuthFlow struct {
	authURL    string
	profileDir string
	client     *acpClient
	doneCh     chan error
	closeOnce  sync.Once
}

func (f *antigravityOAuthFlow) AuthURL() string { return f.authURL }

func (f *antigravityOAuthFlow) Wait(ctx context.Context) (auth.Grant, error) {
	defer f.Close()
	select {
	case err := <-f.doneCh:
		if err != nil {
			return auth.Grant{}, err
		}
	case <-ctx.Done():
		return auth.Grant{}, ctx.Err()
	}

	tokenCandidates := []string{
		filepath.Join(f.profileDir, "antigravity-acp", "acp_token.json"),
		filepath.Join(f.profileDir, "acp_token.json"),
		filepath.Join(f.profileDir, ".gemini", "antigravity-acp", "acp_token.json"),
		filepath.Join(f.profileDir, ".gemini", "acp_token.json"),
	}
	var tokenBytes []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range tokenCandidates {
			if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) > 2 {
				tokenBytes = bytes.TrimSpace(b)
				break
			}
		}
		if len(tokenBytes) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(tokenBytes) == 0 {
		return auth.Grant{}, errors.New("Antigravity ACP did not save OAuth credentials")
	}

	email := fetchAntigravityUserEmail(ctx, tokenBytes)
	tokenStr := string(tokenBytes)
	return auth.Grant{
		Email:        email,
		RefreshToken: tokenStr,
		AccessToken:  tokenStr,
	}, nil
}

func (f *antigravityOAuthFlow) Close() {
	f.closeOnce.Do(func() {
		if f.client != nil {
			f.client.close()
		}
		if f.profileDir != "" {
			_ = os.RemoveAll(f.profileDir)
		}
	})
}

func (r *realAntigravity) StartOAuth(ctx context.Context) (flow, error) {
	rt, err := r.ensureRuntime(ctx)
	if err != nil {
		return nil, err
	}

	profileDir, err := os.MkdirTemp("", "godwit-agy-auth-*")
	if err != nil {
		return nil, err
	}

	env, err := prepareAntigravityProfile(profileDir, rt, "")
	if err != nil {
		_ = os.RemoveAll(profileDir)
		return nil, err
	}

	urlCh := make(chan string, 1)
	client, err := startACPClient(rt.ExecutablePath, profileDir, env, func(rawURL string) {
		select {
		case urlCh <- rawURL:
		default:
		}
	})
	if err != nil {
		_ = os.RemoveAll(profileDir)
		return nil, err
	}

	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = client.initialize(initCtx)
	cancel()
	if err != nil {
		client.close()
		_ = os.RemoveAll(profileDir)
		return nil, err
	}

	doneCh := make(chan error, 1)
	go func() {
		authCtx, authCancel := context.WithTimeout(context.Background(), auth.FlowTimeout)
		defer authCancel()
		_, err := client.call(authCtx, "authenticate", map[string]any{
			"methodId": "oauth-personal",
		})
		doneCh <- err
	}()

	waitCtx, waitCancel := context.WithTimeout(ctx, 20*time.Second)
	defer waitCancel()

	select {
	case rawURL := <-urlCh:
		validURL, err := validateAntigravityAuthURL(rawURL)
		if err != nil {
			client.close()
			_ = os.RemoveAll(profileDir)
			return nil, err
		}
		return &antigravityOAuthFlow{
			authURL:    validURL,
			profileDir: profileDir,
			client:     client,
			doneCh:     doneCh,
		}, nil
	case err := <-doneCh:
		client.close()
		_ = os.RemoveAll(profileDir)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("Antigravity ACP completed authentication without emitting an OAuth URL")
	case <-waitCtx.Done():
		client.close()
		_ = os.RemoveAll(profileDir)
		return nil, errors.New("timed out waiting for Antigravity OAuth URL")
	}
}

// --- ACP JSON-RPC 2.0 Client over stdio ---

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type acpClient struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	mu        sync.Mutex
	writeMu   sync.Mutex
	nextID    atomic.Int64
	pending   map[int64]chan acpMessage
	chunks    map[string][]string
	chunkCBs  map[string]func(string)
	closed    bool
	closeOnce sync.Once
}

func startACPClient(execPath, cwd string, env []string, onAuthURL func(string)) (*acpClient, error) {
	cmd := exec.Command(execPath)
	cmd.Dir = cwd
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, err
	}

	c := &acpClient{
		cmd:      cmd,
		stdin:    stdin,
		pending:  make(map[int64]chan acpMessage),
		chunks:   make(map[string][]string),
		chunkCBs: make(map[string]func(string)),
	}

	go c.readStdout(stdout)
	go c.readStderr(stderr, onAuthURL)
	return c, nil
}

func (c *acpClient) readStderr(r io.Reader, onAuthURL func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if onAuthURL != nil {
			if u := extractAuthURLFromLine(line); u != "" {
				onAuthURL(u)
			}
		}
	}
}

func (c *acpClient) readStdout(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg acpMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Method != "" && msg.ID != nil {
			c.handleServerRequest(msg)
			continue
		}
		if msg.Method != "" {
			c.handleNotification(msg)
			continue
		}
		if id, ok := parseJSONRPCID(msg.ID); ok {
			c.mu.Lock()
			ch, exists := c.pending[id]
			if exists {
				delete(c.pending, id)
			}
			c.mu.Unlock()
			if exists {
				ch <- msg
			}
		}
	}
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func parseJSONRPCID(raw any) (int64, bool) {
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case string:
		i, err := strconv.ParseInt(v, 10, 64)
		return i, err == nil
	}
	return 0, false
}

func (c *acpClient) handleServerRequest(msg acpMessage) {
	if msg.Method == "session/request_permission" {
		var params struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		outcome := map[string]any{
			"outcome": map[string]any{
				"outcome": "cancelled",
			},
		}
		for _, opt := range params.Options {
			if (opt.Kind == "reject_once" || opt.Kind == "reject_always") && opt.OptionID != "" {
				outcome = map[string]any{
					"outcome": map[string]any{
						"outcome":  "selected",
						"optionId": opt.OptionID,
					},
				}
				break
			}
		}
		_ = c.sendJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"result":  outcome,
		})
		return
	}
	_ = c.sendJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      msg.ID,
		"error": map[string]any{
			"code":    -32601,
			"message": "Method not enabled: " + msg.Method,
		},
	})
}

func (c *acpClient) handleNotification(msg acpMessage) {
	if msg.Method != "session/update" {
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return
	}
	if p.Update.SessionUpdate == "agent_message_chunk" && p.Update.Content.Type == "text" && p.Update.Content.Text != "" {
		c.mu.Lock()
		c.chunks[p.SessionID] = append(c.chunks[p.SessionID], p.Update.Content.Text)
		cb := c.chunkCBs[p.SessionID]
		c.mu.Unlock()
		if cb != nil {
			cb(p.Update.Content.Text)
		}
	}
}

func (c *acpClient) sendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(b)
	return err
}

func (c *acpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan acpMessage, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("Antigravity ACP connection closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.sendJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("Antigravity ACP process exited unexpectedly")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("Antigravity ACP error (%d): %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *acpClient) initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
		"clientInfo": map[string]any{
			"name":    "godwit",
			"title":   "Godwit",
			"version": "1.27.0",
		},
	})
	return err
}

func (c *acpClient) close() {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
			_ = c.cmd.Wait()
		}
	})
}

type acpChoice struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type acpConfigOption struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Category     string      `json:"category,omitempty"`
	CurrentValue string      `json:"currentValue,omitempty"`
	Options      []acpChoice `json:"options"`
}

type acpSessionNewResult struct {
	SessionID     string            `json:"sessionId"`
	ConfigOptions []acpConfigOption `json:"configOptions"`
	Models        struct {
		AvailableModels []struct {
			ModelID     string `json:"modelId"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"availableModels"`
		CurrentModelID string `json:"currentModelId"`
	} `json:"models"`
}

func findModelOption(opts []acpConfigOption) *acpConfigOption {
	for i := range opts {
		id := strings.ToLower(opts[i].ID)
		cat := strings.ToLower(opts[i].Category)
		if (id == "model" || cat == "model") && len(opts[i].Options) > 0 {
			return &opts[i]
		}
	}
	return nil
}

func findVariantOption(opts []acpConfigOption) *acpConfigOption {
	for i := range opts {
		id := strings.ToLower(opts[i].ID)
		cat := strings.ToLower(opts[i].Category)
		switch {
		case id == "thinking_level" || id == "thought_level" || id == "thinking" || id == "effort" || id == "variant",
			cat == "thought_level" || cat == "thinking" || cat == "effort" || cat == "variant":
			if len(opts[i].Options) > 0 {
				return &opts[i]
			}
		}
	}
	return nil
}

func (r *realAntigravity) withSession(ctx context.Context, tokenJSON string, fn func(c *acpClient, sess acpSessionNewResult, workDir string) error) error {
	if strings.TrimSpace(tokenJSON) == "" {
		return ai.ErrModelAuth
	}
	rt, err := r.ensureRuntime(ctx)
	if err != nil {
		return err
	}
	profileDir := r.profileDirForToken(tokenJSON)
	env, err := prepareAntigravityProfile(profileDir, rt, tokenJSON)
	if err != nil {
		return err
	}
	workDir, err := os.MkdirTemp("", "godwit-agy-turn-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	client, err := startACPClient(rt.ExecutablePath, workDir, env, nil)
	if err != nil {
		return err
	}
	defer client.close()

	if err := client.initialize(ctx); err != nil {
		return err
	}

	raw, err := client.call(ctx, "session/new", map[string]any{
		"cwd":        workDir,
		"mcpServers": []any{},
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "auth") || strings.Contains(strings.ToLower(err.Error()), "credential") || strings.Contains(strings.ToLower(err.Error()), "login") {
			return ai.ErrModelAuth
		}
		return err
	}
	var sess acpSessionNewResult
	if err := json.Unmarshal(raw, &sess); err != nil {
		return err
	}
	if sess.SessionID == "" {
		return errors.New("Antigravity ACP returned empty sessionId")
	}
	return fn(client, sess, workDir)
}

func (r *realAntigravity) ListModels(ctx context.Context, tokenJSON string) ([]ai.Model, error) {
	var models []ai.Model
	err := r.withSession(ctx, tokenJSON, func(c *acpClient, sess acpSessionNewResult, _ string) error {
		seen := map[string]bool{}
		add := func(id, name string) {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				return
			}
			seen[id] = true
			models = append(models, ai.Model{ID: id, Name: strings.TrimSpace(name)})
		}

		if mOpt := findModelOption(sess.ConfigOptions); mOpt != nil {
			for _, ch := range mOpt.Options {
				add(ch.Value, ch.Name)
			}
		}
		for _, am := range sess.Models.AvailableModels {
			add(am.ModelID, am.Name)
		}
		return nil
	})
	return models, err
}

func resolveModelAndVariant(requested string, opts []acpConfigOption) (baseModel, variantID, variantVal string) {
	req := strings.TrimSpace(requested)
	mOpt := findModelOption(opts)
	vOpt := findVariantOption(opts)

	if base, suffix, ok := strings.Cut(req, "::"); ok && base != "" && suffix != "" {
		if vOpt != nil {
			return base, vOpt.ID, suffix
		}
		return base, "", ""
	}

	if mOpt != nil {
		for _, ch := range mOpt.Options {
			if ch.Value == req {
				return req, "", ""
			}
		}
		if vOpt != nil {
			for _, ch := range mOpt.Options {
				prefix := ch.Value + "-"
				if strings.HasPrefix(req, prefix) {
					vCandidate := strings.TrimPrefix(req, prefix)
					for _, vch := range vOpt.Options {
						if vch.Value == vCandidate {
							return ch.Value, vOpt.ID, vCandidate
						}
					}
				}
			}
		}
	}
	return req, "", ""
}

func (r *realAntigravity) Generate(ctx context.Context, tokenJSON, model, prompt string, onChunk func(string)) (string, error) {
	var reply string
	err := r.withSession(ctx, tokenJSON, func(c *acpClient, sess acpSessionNewResult, _ string) error {
		baseModel, variantID, variantVal := resolveModelAndVariant(model, sess.ConfigOptions)
		if mOpt := findModelOption(sess.ConfigOptions); mOpt != nil && baseModel != "" && mOpt.CurrentValue != baseModel {
			_, _ = c.call(ctx, "session/set_config_option", map[string]any{
				"sessionId": sess.SessionID,
				"configId":  mOpt.ID,
				"value":     baseModel,
			})
		}
		if variantID != "" && variantVal != "" {
			_, _ = c.call(ctx, "session/set_config_option", map[string]any{
				"sessionId": sess.SessionID,
				"configId":  variantID,
				"value":     variantVal,
			})
		}

		c.mu.Lock()
		c.chunks[sess.SessionID] = nil
		if onChunk != nil {
			c.chunkCBs[sess.SessionID] = onChunk
		}
		c.mu.Unlock()

		defer func() {
			c.mu.Lock()
			delete(c.chunkCBs, sess.SessionID)
			delete(c.chunks, sess.SessionID)
			c.mu.Unlock()
		}()

		_, err := c.call(ctx, "session/prompt", map[string]any{
			"sessionId": sess.SessionID,
			"prompt": []map[string]any{
				{
					"type": "text",
					"text": prompt,
				},
			},
		})
		if err != nil {
			return err
		}

		c.mu.Lock()
		reply = strings.Join(c.chunks[sess.SessionID], "")
		c.mu.Unlock()
		return nil
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reply) == "" {
		return "", errors.New("Antigravity ACP returned an empty response")
	}
	return reply, nil
}

func (r *realAntigravity) Judge(ctx context.Context, tokenJSON, model, input string, questions []ai.Question) ([]ai.Answer, error) {
	if model == "" {
		model = "gemini-3.8-flash"
	}
	gen := func(ctx context.Context, system, prompt string) (string, error) {
		full := prompt
		if strings.TrimSpace(system) != "" {
			full = "[System Instructions]\n" + system + "\n\n" + prompt
		}
		return r.Generate(ctx, tokenJSON, model, full, nil)
	}
	judge := ai.NewGenJudge(gen, nil)
	return judge.Ask(ctx, input, questions)
}

// --- Service Integration & In-Memory HTTP Transport ---

func (s *Service) antigravity() Antigravity {
	s.agMu.Lock()
	defer s.agMu.Unlock()
	if s.opts.Antigravity != nil {
		return s.opts.Antigravity
	}
	if s.agRuntime == nil {
		s.agRuntime = newRealAntigravity(s.opts.DataDir)
	}
	return s.agRuntime
}

func (s *Service) startAntigravityOAuth(ctx context.Context) (flow, error) {
	return s.antigravity().StartOAuth(ctx)
}

func (s *Service) antigravityClient() *http.Client {
	s.agMu.Lock()
	defer s.agMu.Unlock()
	if s.antigravityClientVal == nil {
		s.antigravityClientVal = &http.Client{
			Transport: &antigravityTransport{ag: s.antigravity},
		}
	}
	return s.antigravityClientVal
}

type antigravityTransport struct {
	ag func() Antigravity
}

func extractBearerToken(req *http.Request) string {
	authHeader := strings.TrimSpace(req.Header.Get("Authorization"))
	if after, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	if after, ok := strings.CutPrefix(authHeader, "bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func jsonResponse(status int, body any) *http.Response {
	b, _ := json.Marshal(body)
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

func (t *antigravityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	tokenJSON := extractBearerToken(req)
	if tokenJSON == "" {
		return jsonResponse(http.StatusUnauthorized, map[string]any{
			"error": map[string]any{"message": "sign in with Google first", "code": "unauthorized"},
		}), nil
	}

	ag := t.ag()
	path := req.URL.Path

	switch {
	case strings.HasSuffix(path, "/models"):
		ms, err := ag.ListModels(ctx, tokenJSON)
		if errors.Is(err, ai.ErrModelAuth) {
			return jsonResponse(http.StatusUnauthorized, map[string]any{
				"error": map[string]any{"message": err.Error(), "code": "unauthorized"},
			}), nil
		}
		if err != nil {
			return jsonResponse(http.StatusBadGateway, map[string]any{
				"error": map[string]any{"message": err.Error()},
			}), nil
		}
		type item struct {
			ID          string `json:"id"`
			Object      string `json:"object"`
			DisplayName string `json:"display_name,omitempty"`
		}
		data := make([]item, 0, len(ms))
		for _, m := range ms {
			data = append(data, item{ID: m.ID, Object: "model", DisplayName: m.Name})
		}
		return jsonResponse(http.StatusOK, map[string]any{
			"object": "list",
			"data":   data,
		}), nil

	case strings.HasSuffix(path, "/chat/completions"):
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{
				"error": map[string]any{"message": err.Error()},
			}), nil
		}
		var sb strings.Builder
		for _, m := range body.Messages {
			var text string
			switch c := m.Content.(type) {
			case string:
				text = c
			case []any:
				for _, part := range c {
					if pm, ok := part.(map[string]any); ok {
						if tx, ok := pm["text"].(string); ok {
							text += tx
						}
					}
				}
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			if m.Role == "system" || m.Role == "developer" {
				sb.WriteString("[System Instructions]\n")
				sb.WriteString(text)
				sb.WriteString("\n\n")
			} else {
				sb.WriteString(text)
				sb.WriteString("\n")
			}
		}
		prompt := strings.TrimSpace(sb.String())

		if !body.Stream {
			reply, err := ag.Generate(ctx, tokenJSON, body.Model, prompt, nil)
			if errors.Is(err, ai.ErrModelAuth) {
				return jsonResponse(http.StatusUnauthorized, map[string]any{
					"error": map[string]any{"message": err.Error(), "code": "unauthorized"},
				}), nil
			}
			if err != nil {
				return jsonResponse(http.StatusBadGateway, map[string]any{
					"error": map[string]any{"message": err.Error()},
				}), nil
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"id":      "chatcmpl-antigravity",
				"object":  "chat.completion",
				"model":   body.Model,
				"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
			}), nil
		}

		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			writeChunk := func(delta map[string]any, finish any) {
				chunk := map[string]any{
					"id":      "chatcmpl-antigravity",
					"object":  "chat.completion.chunk",
					"model":   body.Model,
					"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
				}
				b, _ := json.Marshal(chunk)
				_, _ = fmt.Fprintf(pw, "data: %s\n\n", b)
			}
			_, err := ag.Generate(ctx, tokenJSON, body.Model, prompt, func(text string) {
				writeChunk(map[string]any{"content": text}, nil)
			})
			if err != nil {
				errObj := map[string]any{"error": map[string]any{"message": err.Error()}}
				b, _ := json.Marshal(errObj)
				_, _ = fmt.Fprintf(pw, "data: %s\n\n", b)
				return
			}
			writeChunk(map[string]any{}, "stop")
			_, _ = io.WriteString(pw, "data: [DONE]\n\n")
		}()

		h := make(http.Header)
		h.Set("Content-Type", "text/event-stream")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     h,
			Body:       pr,
		}, nil

	case strings.HasSuffix(path, "/decisions"):
		var body struct {
			Model     string        `json:"model"`
			Input     string        `json:"input"`
			Questions []ai.Question `json:"questions"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{
				"error": map[string]any{"message": err.Error()},
			}), nil
		}
		ans, err := ag.Judge(ctx, tokenJSON, body.Model, body.Input, body.Questions)
		if errors.Is(err, ai.ErrModelAuth) {
			return jsonResponse(http.StatusUnauthorized, map[string]any{
				"error": map[string]any{"message": err.Error(), "code": "unauthorized"},
			}), nil
		}
		if err != nil {
			return jsonResponse(http.StatusBadGateway, map[string]any{
				"error": map[string]any{"message": err.Error()},
			}), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{"answers": ans}), nil
	}

	return jsonResponse(http.StatusNotFound, map[string]any{
		"error": map[string]any{"message": "not found"},
	}), nil
}

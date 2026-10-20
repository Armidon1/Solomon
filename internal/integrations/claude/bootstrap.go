package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	sidecarsrc "github.com/SAPPHIR3-ROS3/Solomon/v2026/integrations/claude"
	"github.com/SAPPHIR3-ROS3/Solomon/v2026/internal/paths"
	"github.com/gofrs/flock"
)

// runtimeDir extracts the embedded sidecar into Solomon home so installed
// clients, such as the desktop app, run it outside a repository checkout.
func runtimeDir(ctx context.Context) (string, error) {
	home, err := paths.SolomonHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "integrations", "claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	lock := flock.New(filepath.Join(dir, ".install.lock"))
	if err := lock.Lock(); err != nil {
		return "", err
	}
	defer lock.Unlock()
	stale := false
	err = fs.WalkDir(sidecarsrc.Files, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || path == "." {
			return walkErr
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := sidecarsrc.Files.ReadFile(path)
		if err != nil {
			return err
		}
		if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, data) {
			return nil
		}
		if path == "package-lock.json" {
			stale = true
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		return "", fmt.Errorf("extract Claude sidecar: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-agent-sdk")); err != nil || stale {
		if err := npmCI(ctx, dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func installedCLI() string {
	if path, err := exec.LookPath("claude"); err == nil {
		return path
	}
	home, _ := os.UserHomeDir()
	names := []string{filepath.Join(home, ".local", "bin", "claude"), filepath.Join(home, ".claude", "local", "claude"), filepath.Join(home, ".npm-global", "bin", "claude")}
	if runtime.GOOS == "windows" {
		names = append(names, filepath.Join(os.Getenv("APPDATA"), "npm", "claude.cmd"), filepath.Join(home, ".local", "bin", "claude.exe"))
	}
	for _, path := range names {
		if info, err := os.Stat(path); err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0111 != 0) {
			return path
		}
	}
	return ""
}

func bundledCLI(dir string) (string, error) {
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	base := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-agent-sdk-"+platform+"-"+arch)
	if runtime.GOOS == "linux" {
		if matches, _ := filepath.Glob("/lib/ld-musl-*.so.1"); len(matches) > 0 {
			base += "-musl"
		}
	}
	path := filepath.Join(base, name)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", fmt.Errorf("official Claude CLI missing from SDK optional package for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return path, nil
}

// resolveCLI prefers an existing Claude CLI over the SDK's bundled one.
func resolveCLI(ctx context.Context) (string, error) {
	if cli := installedCLI(); cli != "" {
		return cli, nil
	}
	dir, err := sourceDir(ctx)
	if err != nil {
		return "", err
	}
	return bundledCLI(dir)
}

// cliEnv drops API credentials so the CLI always authenticates with the subscription login.
func cliEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN":
			continue
		}
		env = append(env, entry)
	}
	return env
}

type AuthStatus struct {
	LoggedIn   bool   `json:"loggedIn"`
	AuthMethod string `json:"authMethod"`
	Email      string `json:"email"`
}

// Subscription reports whether the login can serve Claude Sub chats.
func (s AuthStatus) Subscription() bool {
	return s.LoggedIn && s.AuthMethod == "claude.ai"
}

func Status(ctx context.Context) (AuthStatus, error) {
	cli, err := resolveCLI(ctx)
	if err != nil {
		return AuthStatus{}, err
	}
	cmd := exec.CommandContext(ctx, cli, "auth", "status", "--json")
	cmd.Env = cliEnv()
	configureProcess(cmd)
	cmd.Cancel = func() error { return killProcess(cmd) }
	// A logged-out CLI exits non-zero but still prints its status.
	out, runErr := cmd.Output()
	var status AuthStatus
	if err := json.Unmarshal(out, &status); err != nil {
		if runErr != nil {
			return AuthStatus{}, fmt.Errorf("official Claude CLI status: %w", runErr)
		}
		return AuthStatus{}, fmt.Errorf("official Claude CLI status: %w", err)
	}
	return status, nil
}

func Login(ctx context.Context, in io.Reader, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cli, err := resolveCLI(ctx)
	if err != nil {
		return err
	}
	if out == nil {
		out = io.Discard
	}
	cmd := exec.CommandContext(ctx, cli, "auth", "login", "--claudeai")
	cmd.Env = cliEnv()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	configureProcess(cmd)
	cmd.Cancel = func() error { return killProcess(cmd) }
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("official Claude CLI login: %w", err)
	}
	status, err := Status(ctx)
	if err != nil {
		return err
	}
	if !status.Subscription() {
		return fmt.Errorf("official Claude CLI login did not produce a Claude subscription session")
	}
	return nil
}

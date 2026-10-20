package test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	authclaude "github.com/SAPPHIR3-ROS3/Solomon/v2026/internal/auth/anthropic/claude"
	"github.com/SAPPHIR3-ROS3/Solomon/v2026/internal/config"
	"github.com/SAPPHIR3-ROS3/Solomon/v2026/internal/integrations/claude"
)

func TestClaudeSidecarSourcesResolveFromRepo(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "integrations", "claude", "src", "index.ts")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("repo root not found")
		}
		root = parent
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = claude.Ensure(ctx, root)
	if err == nil {
		t.Fatal("expected cancelled ensure to fail before serving")
	}
}

func TestClaudeAccessTokenReadsCLICredentialsWithoutRefresh(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if got := claude.AccessToken(); got != "" {
		t.Fatalf("missing credentials token=%q", got)
	}
	write := func(expires time.Time) {
		body := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"cli-access","refreshToken":"cli-refresh","expiresAt":%d}}`, expires.UnixMilli())
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(time.Now().Add(time.Hour))
	if got := claude.AccessToken(); got != "cli-access" {
		t.Fatalf("token=%q", got)
	}
	write(time.Now().Add(-time.Hour))
	if got := claude.AccessToken(); got != "" {
		t.Fatalf("expired token=%q", got)
	}
}

func TestClaudeCLIStatusAndLoginUseSubscription(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	bin := t.TempDir()
	state := filepath.Join(bin, "logged-in")
	script := "#!/bin/sh\n" +
		"[ -n \"$ANTHROPIC_API_KEY\" ] && { echo leaked >&2; exit 3; }\n" +
		"case \"$1 $2\" in\n" +
		"'auth status') if [ -f " + state + " ]; then echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\",\"email\":\"a@b.c\"}'; else echo '{\"loggedIn\":false,\"authMethod\":\"none\"}'; exit 1; fi ;;\n" +
		"'auth login') [ \"$3\" = --claudeai ] && : > " + state + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-cli")
	status, err := claude.Status(t.Context())
	if err != nil || status.Subscription() {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if err := claude.Login(t.Context(), strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	status, err = claude.Status(t.Context())
	if err != nil || !status.Subscription() || status.Email != "a@b.c" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestClaudeSubIsReadyWithoutSolomonTokens(t *testing.T) {
	p, err := config.NewClaudeSubProvider(authclaude.TokenSet{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsClaudeSub() || !config.ProviderCredentialsReady(&p) {
		t.Fatalf("provider=%+v", p)
	}
	got := config.FilterClaudeSubModels([]string{"default", "opus", "claude-opus-5", "claude-opus-5", " claude-haiku-4-5 "})
	if strings.Join(got, ",") != "claude-opus-5,claude-haiku-4-5" {
		t.Fatalf("models=%v", got)
	}
}

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/mcpserver"
	"github.com/spf13/cobra"
)

func executeMCPSetup(ops mcpSetupOperations, args ...string) (string, error) {
	return executeMCPSetupContext(context.Background(), ops, args...)
}

func executeMCPSetupContext(ctx context.Context, ops mcpSetupOperations, args ...string) (string, error) {
	var path string
	root := &cobra.Command{Use: "mcp", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().StringVar(&path, "mcp-config", "", "MCP settings file")
	root.AddCommand(mcpSetupCommand(&path, ops))
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append([]string{"setup"}, args...))
	err := root.ExecuteContext(ctx)
	return output.String(), err
}

func setupWithoutClients(t *testing.T) mcpSetupOperations {
	t.Helper()
	return mcpSetupOperations{
		detect: func() []string { return nil },
		install: func(context.Context, string, string, string) (string, error) {
			t.Fatal("unexpected client registration")
			return "", nil
		},
	}
}

func TestMCPSetupSavesDefaultsAndRegistersOnlyDetectedClients(t *testing.T) {
	isolateMCPConfiguration(t)
	var installed []string
	ops := mcpSetupOperations{
		detect: func() []string { return []string{"claude", "codex"} },
		install: func(_ context.Context, name, endpoint, token string) (string, error) {
			installed = append(installed, name)
			if endpoint != "http://127.0.0.1:8766/mcp" || token != "" {
				t.Fatal("incorrect default endpoint/authentication")
			}
			if _, err := loadMCPSettings(""); err != nil {
				t.Fatal("client registration occurred before settings were saved")
			}
			return "/isolated/" + name, nil
		},
	}
	output, err := executeMCPSetup(ops)
	if err != nil || strings.Join(installed, ",") != "claude,codex" {
		t.Fatalf("installed=%v output=%q err=%v", installed, output, err)
	}
	settings, err := loadMCPSettings("")
	if err != nil || settings.Listen != "127.0.0.1:8766" || settings.TokenFile != "" || settings.NoToken {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
	for _, want := range []string{"Saved MCP settings:", "running as root configures root's clients", "Configured claude:", "Configured codex:", "Setup does not start the server."} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %q", want, output)
		}
	}
}

func TestMCPSetupCanSaveWithoutClientInstallation(t *testing.T) {
	for _, args := range [][]string{{"--client", "none"}, {"--client", "codex", "--install=false"}} {
		isolateMCPConfiguration(t)
		path := filepath.Join(t.TempDir(), "custom.yaml")
		output, err := executeMCPSetup(setupWithoutClients(t), append(args, "--mcp-config", path, "--listen", "0.0.0.0:9001", "--no-token")...)
		if err != nil {
			t.Fatalf("output=%q err=%v", output, err)
		}
		settings, err := loadMCPSettings(path)
		if err != nil || settings.Listen != "0.0.0.0:9001" || !settings.NoToken {
			t.Fatalf("settings=%+v err=%v", settings, err)
		}
	}
}

func TestMCPSetupGeneratesAndReusesTokenWithoutPrintingIt(t *testing.T) {
	isolateMCPConfiguration(t)
	ops := setupWithoutClients(t)
	output, err := executeMCPSetup(ops, "--client", "none", "--token", "--listen", "0.0.0.0:9001")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := loadMCPSettings("")
	if err != nil || settings.TokenFile == "" || settings.NoToken {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
	before, err := os.ReadFile(settings.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(before)
	if strings.Contains(output, strings.TrimSpace(string(before))) {
		t.Fatal("setup printed the bearer token")
	}
	output, err = executeMCPSetup(ops, "--client", "none", "--token")
	after, readErr := os.ReadFile(settings.TokenFile)
	defer clear(after)
	if err != nil || readErr != nil || !bytes.Equal(before, after) {
		t.Fatal("rerunning setup rotated an existing token")
	}
	if strings.Contains(output, strings.TrimSpace(string(before))) {
		t.Fatal("rerun printed the bearer token")
	}
}

func TestMCPSetupPreservesSavedOptionsAndAppliesExplicitOverrides(t *testing.T) {
	isolateMCPConfiguration(t)
	ops := setupWithoutClients(t)
	if _, err := executeMCPSetup(ops, "--client", "none", "--listen", "0.0.0.0:9001", "--no-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeMCPSetup(ops, "--client", "none", "--port", "9002"); err != nil {
		t.Fatal(err)
	}
	saved, err := loadMCPSettings("")
	if err != nil || saved.Listen != "0.0.0.0:9002" || !saved.NoToken {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if _, err := executeMCPSetup(ops, "--client", "none", "--token"); err != nil {
		t.Fatal(err)
	}
	saved, err = loadMCPSettings("")
	if err != nil || saved.TokenFile == "" || saved.NoToken {
		t.Fatalf("token did not override no-token: %+v err=%v", saved, err)
	}
	if _, err := executeMCPSetup(ops, "--client", "none", "--listen", "127.0.0.1:9003", "--no-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeMCPSetup(ops, "--client", "none", "--no-token=false"); err != nil {
		t.Fatal(err)
	}
	saved, err = loadMCPSettings("")
	if err != nil || saved.Listen != "127.0.0.1:9003" || saved.TokenFile != "" || saved.NoToken {
		t.Fatalf("false override or token clearing failed: %+v err=%v", saved, err)
	}
}

func TestMCPSetupDetectionIgnoresDamagedSettingsAndDoesNotWrite(t *testing.T) {
	home := isolateMCPConfiguration(t)
	path := filepath.Join(home, "bad.yaml")
	data := []byte("broken: [")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ops := setupWithoutClients(t)
	ops.detect = func() []string { return []string{"opencode"} }
	output, err := executeMCPSetup(ops, "--mcp-config", path, "--detect", "--token")
	after, readErr := os.ReadFile(path)
	if err != nil || readErr != nil || !bytes.Equal(data, after) || !strings.Contains(output, "opencode") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(home, "mcp.token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("detect created a token")
	}
}

func TestMCPSetupGuideUsesSavedListenerAndAuthWithoutWriting(t *testing.T) {
	home := isolateMCPConfiguration(t)
	path := filepath.Join(home, "server.yaml")
	data := []byte("listen: 0.0.0.0:9012\nno_token: true\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := executeMCPSetup(setupWithoutClients(t), "--mcp-config", path, "--guide", "--token", "--client", "codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Listener: 0.0.0.0:9012", "Local endpoint: http://127.0.0.1:9012/mcp", "Remote endpoint: http://YOUR-PROXMOX-HOST:9012/mcp", "Authentication: bearer token", "[mcp_servers.pvedisk]", "no files changed"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %q", want, output)
		}
	}
	if strings.Contains(output, "http://0.0.0.0") || strings.Contains(output, "http://127.0.0.1:8766") {
		t.Fatalf("guide ignored selected listener: %q", output)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(data, after) {
		t.Fatal("guide changed saved settings")
	}
	if _, err := os.Stat(filepath.Join(home, "mcp.token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("guide generated a token")
	}
}

func TestMCPSetupRejectsZeroPortAndInvalidClientsWithoutSaving(t *testing.T) {
	for _, args := range [][]string{{"--port", "0", "--token"}, {"--listen", "127.0.0.1:0", "--token"}, {"--listen", "127.0.0.1:00", "--token"}, {"--client", "unknown"}, {"--token", "--no-token"}} {
		home := isolateMCPConfiguration(t)
		if _, err := executeMCPSetup(setupWithoutClients(t), args...); err == nil {
			t.Fatalf("accepted invalid setup: %v", args)
		}
		if _, err := os.Stat(filepath.Join(home, "config", "pvedisk")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid setup wrote files: %v", err)
		}
	}
}

func TestMCPSetupReportsPartialRegistrationAfterSavingSettings(t *testing.T) {
	isolateMCPConfiguration(t)
	var attempted []string
	ops := mcpSetupOperations{detect: func() []string { return []string{"claude", "codex"} }, install: func(_ context.Context, name, endpoint, token string) (string, error) {
		attempted = append(attempted, name)
		if name == "claude" {
			return "", errors.New("invalid client configuration")
		}
		return "/isolated/codex", nil
	}}
	output, err := executeMCPSetup(ops)
	if err == nil || !strings.Contains(err.Error(), "MCP settings saved") || !strings.Contains(err.Error(), "configure claude") || strings.Join(attempted, ",") != "claude,codex" || !strings.Contains(output, "Configured codex") {
		t.Fatalf("attempted=%v output=%q err=%v", attempted, output, err)
	}
	if _, err := loadMCPSettings(""); err != nil {
		t.Fatal("partial client failure lost saved server settings")
	}
}

func TestMCPSetupMissingNamedClientStillSavesSettings(t *testing.T) {
	isolateMCPConfiguration(t)
	_, err := executeMCPSetup(setupWithoutClients(t), "--client", "claude")
	if err == nil || !strings.Contains(err.Error(), "settings saved") || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("unexpected missing-client error: %v", err)
	}
	if _, err := loadMCPSettings(""); err != nil {
		t.Fatal(err)
	}
}

func TestMCPSetupStartCommandQuotesShellMetacharacters(t *testing.T) {
	isolateMCPConfiguration(t)
	path := filepath.Join(t.TempDir(), "quote'$(printf injected).yaml")
	output, err := executeMCPSetup(setupWithoutClients(t), "--client", "none", "--mcp-config", path)
	if err != nil {
		t.Fatal(err)
	}
	quoted := mcpShellQuote(path)
	if !strings.Contains(output, "--mcp-config "+quoted) {
		t.Fatal("generated start command did not quote its settings path")
	}
	value, err := exec.Command("sh", "-c", "printf '%s' "+quoted).Output()
	if err != nil || string(value) != path {
		t.Fatalf("generated path did not survive shell parsing: %q err=%v", value, err)
	}
}

func TestMCPSetupDoesNotStartServerOrLoadSiteConfiguration(t *testing.T) {
	isolateMCPConfiguration(t)
	ops := testOperations()
	ops.load = func(string) (config.Config, error) {
		t.Fatal("MCP setup loaded site configuration")
		return config.Config{}, nil
	}
	ops.mcp = func(context.Context, string, mcpserver.HTTPOptions, io.Writer) error {
		t.Fatal("MCP setup started a server")
		return nil
	}
	if _, err := execute(ops, "mcp", "setup", "--client", "none"); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledMCPSetupDoesNotCreateSettingsOrTokens(t *testing.T) {
	home := isolateMCPConfiguration(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ops := setupWithoutClients(t)
	ops.detect = func() []string { t.Fatal("canceled setup inspected clients"); return nil }
	_, err := executeMCPSetupContext(ctx, ops, "--token")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled setup created settings or token directories")
	}
}

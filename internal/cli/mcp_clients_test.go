package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
)

func mcpClientTestHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"HOME": home, "PATH": bin, "CLAUDE_CONFIG_DIR": "", "CODEX_HOME": "", "XDG_CONFIG_HOME": "", "OPENCODE_CONFIG": ""} {
		t.Setenv(key, value)
	}
	return home, bin
}

func writeClientFixture(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func fakeMCPExecutable(t *testing.T, bin, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
}

func clientJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := hujson.Standardize(data)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(standard))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestDetectedMCPClientsUsesPATHOnly(t *testing.T) {
	_, bin := mcpClientTestHome(t)
	if got := detectedMCPClients(); len(got) != 0 {
		t.Fatalf("detected clients outside PATH: %v", got)
	}
	for _, name := range []string{"codex", "claude", "opencode"} {
		fakeMCPExecutable(t, bin, name, "exit 99")
	}
	if got := detectedMCPClients(); !reflect.DeepEqual(got, []string{"opencode", "claude", "codex"}) {
		t.Fatalf("detected clients: %v", got)
	}
}

func TestClaudeRegistrationPreservesJSONCAndOtherServers(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "home", true: "override"}[override], func(t *testing.T) {
			home, _ := mcpClientTestHome(t)
			dir := home
			if override {
				dir = filepath.Join(home, "custom-claude")
				t.Setenv("CLAUDE_CONFIG_DIR", dir)
			}
			path := filepath.Join(dir, ".claude.json")
			writeClientFixture(t, path, `{
  // Keep this personal setting and its exact large integer.
  "counter": 9007199254740993,
  "preferences": {"theme": "dark"},
  "mcpServers": {
    "qmcp": {"type":"http","url":"http://127.0.0.1:8765/mcp","headers":{"Authorization":"Bearer keep-qmp"}},
    "pvedisk": {"command":"obsolete"},
  },
}`)
			before := clientJSON(t, path)
			location, err := installMCPClient(context.Background(), "claude", "http://127.0.0.1:8766/mcp", "private-pvedisk-token")
			if err != nil || location != path {
				t.Fatalf("register Claude: %s %v", location, err)
			}
			after := clientJSON(t, path)
			if !reflect.DeepEqual(before["preferences"], after["preferences"]) || after["counter"] != json.Number("9007199254740993") {
				t.Fatal("changed unrelated values or number precision")
			}
			servers := after["mcpServers"].(map[string]any)
			if !reflect.DeepEqual(before["mcpServers"].(map[string]any)["qmcp"], servers["qmcp"]) {
				t.Fatal("changed qmcp registration")
			}
			entry := servers["pvedisk"].(map[string]any)
			if entry["type"] != "http" || entry["url"] != "http://127.0.0.1:8766/mcp" || entry["headers"].(map[string]any)["Authorization"] != "Bearer private-pvedisk-token" || entry["command"] != nil {
				t.Fatalf("wrong Claude entry: %v", entry)
			}
			data, _ := os.ReadFile(path)
			if !strings.Contains(string(data), "Keep this personal setting") {
				t.Fatal("lost JSONC comments")
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("client config permissions: %v %v", info, err)
			}
			if _, err := installMCPClient(context.Background(), "claude", "http://127.0.0.1:8766/mcp", ""); err != nil {
				t.Fatal(err)
			}
			entry = clientJSON(t, path)["mcpServers"].(map[string]any)["pvedisk"].(map[string]any)
			if _, exists := entry["headers"]; exists {
				t.Fatal("unauthenticated registration retained old headers")
			}
		})
	}
}

func TestCodexRegistrationPreservesTOMLValues(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "home", true: "override"}[override], func(t *testing.T) {
			home, _ := mcpClientTestHome(t)
			dir := filepath.Join(home, ".codex")
			if override {
				dir = filepath.Join(home, "custom-codex")
				t.Setenv("CODEX_HOME", dir)
			}
			path := filepath.Join(dir, "config.toml")
			writeClientFixture(t, path, `# Existing formatting belongs in the private backup.
model = "keep-model"
counter = 9223372036854775807
date = 2026-11-04
[projects."/some/project"]
trust_level = "trusted"
[mcp_servers.qmcp]
url = "http://127.0.0.1:8765/mcp"
enabled = false
[mcp_servers.pvedisk]
command = "obsolete"
`)
			beforeData, _ := os.ReadFile(path)
			before := map[string]any{}
			if err := toml.Unmarshal(beforeData, &before); err != nil {
				t.Fatal(err)
			}
			location, err := installMCPClient(context.Background(), "codex", "http://127.0.0.1:8766/mcp", "codex-token")
			if err != nil || location != path {
				t.Fatalf("register Codex: %s %v", location, err)
			}
			data, _ := os.ReadFile(path)
			after := map[string]any{}
			if err := toml.Unmarshal(data, &after); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"model", "counter", "date", "projects"} {
				if !reflect.DeepEqual(before[key], after[key]) {
					t.Fatalf("changed TOML value %s", key)
				}
			}
			backups, err := filepath.Glob(path + ".backup-*")
			if err != nil || len(backups) != 1 {
				t.Fatalf("missing original TOML backup: %v %v", backups, err)
			}
			backupData, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(backupData, beforeData) {
				t.Fatalf("TOML backup did not preserve original formatting/comments: %v", err)
			}
			if info, err := os.Stat(backups[0]); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("TOML backup is not private: %v", err)
			}
			servers := after["mcp_servers"].(map[string]any)
			if !reflect.DeepEqual(before["mcp_servers"].(map[string]any)["qmcp"], servers["qmcp"]) {
				t.Fatal("changed qmcp TOML")
			}
			entry := servers["pvedisk"].(map[string]any)
			if entry["url"] != "http://127.0.0.1:8766/mcp" || entry["enabled"] != true || entry["http_headers"].(map[string]any)["Authorization"] != "Bearer codex-token" || entry["command"] != nil {
				t.Fatalf("wrong Codex entry: %v", entry)
			}
			if _, err := installMCPClient(context.Background(), "codex", "http://127.0.0.1:8766/mcp", ""); err != nil {
				t.Fatal(err)
			}
			data, _ = os.ReadFile(path)
			after = map[string]any{}
			if err := toml.Unmarshal(data, &after); err != nil {
				t.Fatal(err)
			}
			if _, exists := after["mcp_servers"].(map[string]any)["pvedisk"].(map[string]any)["http_headers"]; exists {
				t.Fatal("unauthenticated registration retained Codex headers")
			}
		})
	}
}

func TestOpenCodeRegistrationVersionAndExistingJSONC(t *testing.T) {
	for _, version := range []string{"1.5.0", "v2.1.2"} {
		for _, override := range []bool{false, true} {
			t.Run(version+map[bool]string{false: "-xdg", true: "-explicit"}[override], func(t *testing.T) {
				home, bin := mcpClientTestHome(t)
				fakeMCPExecutable(t, bin, "opencode", "printf '%s\\n' '"+version+"'")
				base := filepath.Join(home, "xdg")
				t.Setenv("XDG_CONFIG_HOME", base)
				path := filepath.Join(base, "opencode", "opencode.jsonc")
				if override {
					path = filepath.Join(home, "chosen-opencode.jsonc")
					t.Setenv("OPENCODE_CONFIG", path)
				}
				text := `{"count":9007199254740993, /* keep OpenCode comment */ "mcp":{"qmcp":{"type":"remote","url":"keep-qmp"}}}`
				if strings.Contains(version, "2.") {
					text = `{"count":9007199254740993, /* keep OpenCode comment */ "mcp":{"other_setting":true,"servers":{"qmcp":{"type":"remote","url":"keep-qmp"}}}}`
				}
				writeClientFixture(t, path, text)
				before := clientJSON(t, path)
				location, err := installMCPClient(context.Background(), "opencode", "http://127.0.0.1:8766/mcp", "open-token")
				if err != nil || location != path {
					t.Fatalf("register OpenCode: %s %v", location, err)
				}
				after := clientJSON(t, path)
				servers := after["mcp"].(map[string]any)
				beforeServers := before["mcp"].(map[string]any)
				if strings.Contains(version, "2.") {
					if servers["other_setting"] != true {
						t.Fatal("lost v2 unrelated MCP setting")
					}
					servers = servers["servers"].(map[string]any)
					beforeServers = beforeServers["servers"].(map[string]any)
				}
				if !reflect.DeepEqual(servers["qmcp"], beforeServers["qmcp"]) {
					t.Fatal("changed QMP entry")
				}
				entry := servers["pvedisk"].(map[string]any)
				if entry["type"] != "remote" || entry["url"] != "http://127.0.0.1:8766/mcp" || entry["oauth"] != false || entry["headers"].(map[string]any)["Authorization"] != "Bearer open-token" {
					t.Fatalf("wrong OpenCode entry %v", entry)
				}
				if strings.Contains(version, "2.") {
					if entry["disabled"] != false || entry["enabled"] != nil {
						t.Fatal("incorrect v2 enablement")
					}
				} else if entry["enabled"] != true || entry["disabled"] != nil {
					t.Fatal("incorrect v1 enablement")
				}
				data, _ := os.ReadFile(path)
				if !strings.Contains(string(data), "keep OpenCode comment") || after["count"] != json.Number("9007199254740993") {
					t.Fatal("lost comments or number precision")
				}
				if !override {
					if _, err := os.Stat(filepath.Join(base, "opencode", "opencode.json")); !os.IsNotExist(err) {
						t.Fatal("created a competing OpenCode JSON config")
					}
				}
				if _, err := installMCPClient(context.Background(), "opencode", "http://127.0.0.1:8766/mcp", ""); err != nil {
					t.Fatal(err)
				}
				servers = clientJSON(t, path)["mcp"].(map[string]any)
				if strings.Contains(version, "2.") {
					servers = servers["servers"].(map[string]any)
				}
				if _, exists := servers["pvedisk"].(map[string]any)["headers"]; exists {
					t.Fatal("unauthenticated OpenCode registration retained old headers")
				}
			})
		}
	}
}

func TestNewMCPClientConfigurationPaths(t *testing.T) {
	for _, name := range []string{"claude", "codex", "opencode"} {
		t.Run(name, func(t *testing.T) {
			home, bin := mcpClientTestHome(t)
			fakeMCPExecutable(t, bin, "opencode", "printf '2.0.0\\n'")
			want := filepath.Join(home, ".claude.json")
			if name == "codex" {
				want = filepath.Join(home, ".codex", "config.toml")
			}
			if name == "opencode" {
				want = filepath.Join(home, ".config", "opencode", "opencode.json")
			}
			got, err := installMCPClient(context.Background(), name, "http://127.0.0.1:8766/mcp", "")
			if err != nil || got != want {
				t.Fatalf("new client path %s: %s %v", name, got, err)
			}
			data, err := os.ReadFile(got)
			if err != nil || !bytes.Contains(data, []byte("pvedisk")) || bytes.Contains(data, []byte("Authorization")) {
				t.Fatalf("new client config: %s %v", data, err)
			}
		})
	}
}

func TestCanceledMCPClientMergeDoesNotWrite(t *testing.T) {
	home, _ := mcpClientTestHome(t)
	path := filepath.Join(home, ".claude.json")
	before := `{"mcpServers":{"qmcp":{"url":"keep"}}}`
	writeClientFixture(t, path, before)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mergeMCPClientJSON(ctx, path, []string{"mcpServers"}, map[string]any{"url": "http://127.0.0.1:8766/mcp"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("prewrite cancellation: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != before {
		t.Fatalf("cancelled merge changed client config: %v", err)
	}
	backups, err := filepath.Glob(path + ".backup-*")
	if err != nil || len(backups) != 0 {
		t.Fatalf("cancelled merge wrote a backup: %v %v", backups, err)
	}
}

func TestOpenCodeDoesNotBypassExistingJSONCSymlink(t *testing.T) {
	home, bin := mcpClientTestHome(t)
	fakeMCPExecutable(t, bin, "opencode", "printf '1.0.0\\n'")
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "missing-target"), filepath.Join(dir, "opencode.jsonc")); err != nil {
		t.Fatal(err)
	}
	if _, err := installMCPClient(context.Background(), "opencode", "http://127.0.0.1:8766/mcp", ""); err == nil {
		t.Fatal("accepted a client config symlink")
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.json")); !os.IsNotExist(err) {
		t.Fatal("created competing JSON config around an existing JSONC symlink")
	}
}

func TestMCPClientWriteRejectsConcurrentSourceChange(t *testing.T) {
	for _, scenario := range []string{"edited", "replaced", "created", "no-op-edited"} {
		t.Run(scenario, func(t *testing.T) {
			home, _ := mcpClientTestHome(t)
			path := filepath.Join(home, ".claude.json")
			if scenario != "created" {
				writeClientFixture(t, path, `{"mcpServers":{"qmcp":{"url":"original"}}}`)
			}
			old, original, err := readMCPPrivateSnapshot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(old)
			concurrent := `{"mcpServers":{"qmcp":{"url":"changed-concurrently"}},"new_setting":true}`
			if scenario == "replaced" {
				replacement := path + ".replacement"
				writeClientFixture(t, replacement, concurrent)
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			} else {
				writeClientFixture(t, path, concurrent)
			}
			data := []byte(`{"mcpServers":{"pvedisk":{"url":"http://127.0.0.1:8766/mcp"}}}`)
			if scenario == "no-op-edited" {
				data = append([]byte(nil), old...)
			}
			if err := writeMCPClientConfig(context.Background(), path, data, old, original); err == nil {
				t.Fatal("accepted a stale client configuration snapshot")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != concurrent {
				t.Fatalf("overwrote concurrent client edits: %s %v", got, err)
			}
		})
	}
}

func TestMCPClientInvalidDocumentsRemainUnchanged(t *testing.T) {
	for _, test := range []struct{ name, text string }{{"claude", `{"mcpServers":`}, {"claude", `{"mcpServers":false}`}, {"claude", `[]`}, {"codex", "[broken"}, {"codex", `mcp_servers="invalid"`}, {"opencode", `{"mcp":{"servers":false}}`}} {
		t.Run(test.name+test.text, func(t *testing.T) {
			home, bin := mcpClientTestHome(t)
			path := filepath.Join(home, ".claude.json")
			if test.name == "codex" {
				path = filepath.Join(home, ".codex", "config.toml")
			}
			if test.name == "opencode" {
				fakeMCPExecutable(t, bin, "opencode", "printf '2.0.0\\n'")
				path = filepath.Join(home, "opencode.jsonc")
				t.Setenv("OPENCODE_CONFIG", path)
			}
			writeClientFixture(t, path, test.text)
			if _, err := installMCPClient(context.Background(), test.name, "http://127.0.0.1:8766/mcp", "token"); err == nil {
				t.Fatal("accepted malformed client config")
			}
			data, _ := os.ReadFile(path)
			if string(data) != test.text {
				t.Fatal("malformed config was modified")
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Name(), "backup") {
					t.Fatal("created backup for a rejected document")
				}
			}
		})
	}
}

func TestMCPClientCancellationAndUnknownVersionsDoNotWrite(t *testing.T) {
	home, bin := mcpClientTestHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{"claude", "codex", "opencode"} {
		if _, err := installMCPClient(ctx, name, "http://127.0.0.1:8766/mcp", "token"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	}
	for _, version := range []string{"3.0.0", "unknown"} {
		fakeMCPExecutable(t, bin, "opencode", "printf '%s\\n' '"+version+"'")
		if _, err := installMCPClient(context.Background(), "opencode", "http://127.0.0.1:8766/mcp", ""); err == nil {
			t.Fatal("accepted unsupported OpenCode version")
		}
	}
	fakeMCPExecutable(t, bin, "opencode", "exec /bin/sleep 10")
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := installMCPClient(ctx, "opencode", "http://127.0.0.1:8766/mcp", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("OpenCode version cancellation: %v", err)
	}
	if _, err := installMCPClient(context.Background(), "unknown", "http://127.0.0.1:8766/mcp", ""); err == nil {
		t.Fatal("accepted unsupported client")
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "bin" {
		t.Fatalf("failed registration modified client files: %v", entries)
	}
}

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
)

const mcpClientEntryName = "pvedisk"

func detectedMCPClients() []string {
	var clients []string
	for _, name := range []string{"opencode", "claude", "codex"} {
		if _, err := exec.LookPath(name); err == nil {
			clients = append(clients, name)
		}
	}
	return clients
}

func installMCPClient(ctx context.Context, name, endpoint, token string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if name != "claude" && name != "codex" && name != "opencode" {
		return "", errors.New("your MCP client must be claude, codex, or opencode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	if name == "codex" {
		dir := os.Getenv("CODEX_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".codex")
		}
		path, err := filepath.Abs(filepath.Join(dir, "config.toml"))
		if err != nil {
			return "", err
		}
		document := map[string]any{}
		old, original, err := readMCPPrivateSnapshot(path)
		if err != nil {
			return path, err
		}
		defer clear(old)
		if original != nil {
			if err := toml.Unmarshal(old, &document); err != nil {
				return path, errors.New("your Codex TOML is invalid; no changes made")
			}
		}
		servers, ok := document["mcp_servers"].(map[string]any)
		if !ok {
			if _, exists := document["mcp_servers"]; exists {
				return path, errors.New("your mcp_servers must be a table; no changes made")
			}
			servers = map[string]any{}
			document["mcp_servers"] = servers
		}
		entry := map[string]any{"url": endpoint, "enabled": true}
		if token != "" {
			entry["http_headers"] = headers
		}
		servers[mcpClientEntryName] = entry
		// The TOML encoder preserves values, while the shared writer preserves
		// original bytes in a private backup before normalizing formatting.
		data, err := toml.Marshal(document)
		if err != nil {
			return path, err
		}
		return path, writeMCPClientConfig(ctx, path, data, old, original)
	}
	path := ""
	keys := []string{"mcpServers"}
	entry := map[string]any{"type": "http", "url": endpoint}
	if token != "" {
		entry["headers"] = headers
	}
	if name == "claude" {
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			path = filepath.Join(dir, ".claude.json")
		} else {
			path = filepath.Join(home, ".claude.json")
		}
	} else {
		versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		command := exec.CommandContext(versionCtx, "opencode", "--version")
		command.WaitDelay = 100 * time.Millisecond
		output, err := command.Output()
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("cannot determine your OpenCode version; no client changes made")
		}
		major := strings.Split(strings.TrimPrefix(strings.TrimSpace(string(output)), "v"), ".")[0]
		if major != "1" && major != "2" {
			return "", errors.New("your OpenCode version is unsupported; expected v1 or v2")
		}
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		path = filepath.Join(base, "opencode", "opencode.json")
		if override := os.Getenv("OPENCODE_CONFIG"); override != "" {
			path = override
		} else if _, err := os.Lstat(strings.TrimSuffix(path, filepath.Ext(path)) + ".jsonc"); err == nil {
			path = strings.TrimSuffix(path, filepath.Ext(path)) + ".jsonc"
		} else if !errors.Is(err, os.ErrNotExist) {
			return path, err
		}
		entry = map[string]any{"type": "remote", "url": endpoint, "oauth": false}
		keys = []string{"mcp"}
		if major == "1" {
			entry["enabled"] = true
		} else {
			keys = append(keys, "servers")
			entry["disabled"] = false
		}
		if token != "" {
			entry["headers"] = headers
		}
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return path, mergeMCPClientJSON(ctx, path, keys, entry)
}

func writeMCPClientConfig(ctx context.Context, path string, data, old []byte, original os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeMCPPrivateFileFromSnapshot(path, data, old, original)
}

// mergeMCPClientJSON changes only our entry, retaining comments and exact numbers.
func mergeMCPClientJSON(ctx context.Context, path string, keys []string, entry map[string]any) error {
	old, original, err := readMCPPrivateSnapshot(path)
	if err != nil {
		return err
	}
	defer clear(old)
	data := append([]byte(nil), old...)
	if original == nil {
		data = []byte("{}")
	}
	document, err := hujson.Parse(data)
	if err != nil {
		return errors.New("your client JSON configuration is invalid; no changes made")
	}
	if _, ok := document.Value.(*hujson.Object); !ok {
		return errors.New("your client configuration must be an object; no changes made")
	}
	pointer := ""
	for _, key := range keys {
		pointer += "/" + key
		child := document.Find(pointer)
		if child == nil {
			patch, _ := json.Marshal([]map[string]any{{"op": "add", "path": pointer, "value": map[string]any{}}})
			if err := document.Patch(patch); err != nil {
				return err
			}
		} else if _, ok := child.Value.(*hujson.Object); !ok {
			return fmt.Errorf("your %s configuration must be an object; no changes made", key)
		}
	}
	patch, err := json.Marshal([]map[string]any{{"op": "add", "path": pointer + "/" + mcpClientEntryName, "value": entry}})
	if err != nil {
		return err
	}
	if err := document.Patch(patch); err != nil {
		return err
	}
	document.Format()
	return writeMCPClientConfig(ctx, path, document.Pack(), old, original)
}

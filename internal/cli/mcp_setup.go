package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeeftor/pvedisk/internal/mcpserver"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

type mcpSetupOperations struct {
	detect  func() []string
	install func(context.Context, string, string, string) (string, error)
}

func mcpSetupCommand(settingsPath *string, ops mcpSetupOperations) *cobra.Command {
	var client string
	var port int
	var token, guide, detect, install bool
	var flags mcpSettings
	command := &cobra.Command{
		Use: "setup", Short: "Save your MCP settings and register installed clients",
		Long: "Save persistent MCP server settings and register pvedisk in your installed Claude, Codex, and OpenCode clients. Client files belong to your current account: running as root configures root's clients. Existing settings and tokens are reused unless explicitly overridden. Setup never starts the server. Use --detect or --guide for read-only output.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			if !validMCPClient(client) {
				return errors.New("your client must be all, none, claude, codex, or opencode")
			}
			clients := ops.detect()
			if detect {
				return printDetectedMCPClients(cmd, clients)
			}
			path, err := mcpSettingsPath(*settingsPath)
			if err != nil {
				return err
			}
			settings, originalData, originalInfo, err := loadMCPSettingsSnapshot(*settingsPath)
			defer clear(originalData)
			if errors.Is(err, os.ErrNotExist) {
				settings = mcpSettings{Listen: "127.0.0.1:8766"}
			} else if err != nil {
				return err
			}
			settings = overrideMCPSettings(cmd, settings, flags)
			if cmd.Flags().Changed("port") {
				if port < 1 || port > 65535 {
					return errors.New("your saved MCP port must be between 1 and 65535")
				}
				host, _, err := net.SplitHostPort(settings.Listen)
				if err != nil {
					return errors.New("your MCP listen address must contain a host and port")
				}
				settings.Listen = net.JoinHostPort(host, strconv.Itoa(port))
			}
			if settings.TokenFile != "" {
				settings.TokenFile, err = filepath.Abs(settings.TokenFile)
				if err != nil {
					return err
				}
			}
			if token {
				settings.NoToken = false
				if settings.TokenFile == "" {
					settings.TokenFile = filepath.Join(filepath.Dir(path), "mcp.token")
				}
			}
			if err := settings.validate(); err != nil {
				return err
			}
			_, savedPort, _ := net.SplitHostPort(settings.Listen)
			portNumber, _ := strconv.Atoi(savedPort)
			if portNumber == 0 {
				return errors.New("your saved MCP listener needs a fixed nonzero port")
			}
			if guide {
				return printMCPGuide(cmd, client, path, settings)
			}
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			if token {
				if err := ensureMCPToken(settings.TokenFile); err != nil {
					return fmt.Errorf("prepare your MCP token: %w", err)
				}
			}
			var secret []byte
			if settings.TokenFile != "" {
				secret, err = mcpserver.ReadToken(settings.TokenFile)
				if err != nil {
					return err
				}
				defer clear(secret)
			}
			data, err := yaml.Marshal(settings)
			if err != nil {
				return err
			}
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			if err := writeMCPPrivateFileFromSnapshot(path, data, originalData, originalInfo); err != nil {
				return fmt.Errorf("save your MCP settings: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved MCP settings: %s\n", path)
			if err := printDetectedMCPClients(cmd, clients); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Client configuration belongs to your current account; running as root configures root's clients.")
			endpoint := mcpLocalEndpoint(settings.Listen)
			if install && client != "none" {
				selected := clients
				if client != "all" {
					if !containsMCPClient(clients, client) {
						return fmt.Errorf("MCP settings saved; your %s client was not found on PATH", client)
					}
					selected = []string{client}
				}
				var failures []error
				for _, name := range selected {
					location, err := ops.install(cmd.Context(), name, endpoint, string(secret))
					if err != nil {
						failures = append(failures, fmt.Errorf("configure %s: %w", name, err))
						continue
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Configured %s: %s\n", name, location)
				}
				if err := errors.Join(failures...); err != nil {
					return fmt.Errorf("MCP settings saved; some clients need attention: %w", err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Start your server: pvedisk mcp --mcp-config %s\nLocal endpoint: %s\nRestart or reconnect your clients, then inspect list_vms or version. Setup does not start the server.\n", mcpShellQuote(path), endpoint)
			return nil
		},
	}
	command.Flags().StringVar(&client, "client", "all", "Client to configure: all detected clients, none, claude, codex, or opencode")
	command.Flags().IntVar(&port, "port", 8766, "Override the saved listener port")
	command.Flags().BoolVar(&token, "token", false, "Generate or reuse a private bearer token")
	command.Flags().BoolVar(&guide, "guide", false, "Print connection instructions without changing files")
	command.Flags().BoolVar(&detect, "detect", false, "List installed clients without reading settings or changing files")
	command.Flags().BoolVar(&install, "install", true, "Register pvedisk in your selected installed clients")
	command.Flags().StringVar(&flags.Listen, "listen", "127.0.0.1:8766", "Saved listener host and port")
	command.Flags().StringVar(&flags.TokenFile, "token-file", "", "Existing bearer token file; use --token to generate if missing")
	command.Flags().BoolVar(&flags.NoToken, "no-token", false, "Save explicit tokenless access, including on your LAN")
	command.MarkFlagsMutuallyExclusive("token", "no-token")
	command.MarkFlagsMutuallyExclusive("token-file", "no-token")
	command.MarkFlagsMutuallyExclusive("guide", "detect")
	return command
}

func validMCPClient(client string) bool {
	return client == "all" || client == "none" || client == "claude" || client == "codex" || client == "opencode"
}

func containsMCPClient(clients []string, name string) bool {
	for _, client := range clients {
		if client == name {
			return true
		}
	}
	return false
}

func printDetectedMCPClients(cmd *cobra.Command, clients []string) error {
	value := strings.Join(clients, ", ")
	if value == "" {
		value = "none"
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Detected clients on PATH: %s\n", value)
	return err
}

func mcpLocalEndpoint(listen string) string {
	host, port, _ := net.SplitHostPort(listen)
	if host == "" || net.ParseIP(host).IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/mcp"
}

func printMCPGuide(cmd *cobra.Command, client, path string, settings mcpSettings) error {
	endpoint := mcpLocalEndpoint(settings.Listen)
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "pvedisk MCP connection guide (no files changed)\nSettings: %s\nListener: %s\nLocal endpoint: %s\n", path, settings.Listen, endpoint)
	if settings.TokenFile != "" {
		fmt.Fprintf(out, "Authentication: bearer token from %s (token value is not printed)\nSet your client's Authorization header to Bearer followed by the token file contents.\n", settings.TokenFile)
	} else {
		fmt.Fprintln(out, "Authentication: no bearer token")
	}
	fmt.Fprintln(out, "To create settings or apply the options shown here, rerun setup with the same options without --guide.")
	fmt.Fprintf(out, "Then start your server: pvedisk mcp --mcp-config %s\n", mcpShellQuote(path))
	host, port, _ := net.SplitHostPort(settings.Listen)
	if host == "" || net.ParseIP(host).IsUnspecified() {
		fmt.Fprintf(out, "Remote endpoint: http://YOUR-PROXMOX-HOST:%s/mcp\n", port)
	} else if host == "localhost" || net.ParseIP(host).IsLoopback() {
		fmt.Fprintf(out, "For a client on another machine, keep an SSH forward open to this listener: ssh -N -L %s USER@YOUR-PROXMOX-HOST\nThen use %s on your client machine.\n", mcpShellQuote(port+":"+net.JoinHostPort(host, port)), endpoint)
	} else {
		fmt.Fprintf(out, "Remote endpoint: %s\n", endpoint)
	}
	fmt.Fprintln(out, "Client examples below use your local endpoint. On another machine, use the remote endpoint above or keep the SSH forward open.")
	clients := []string{"claude", "codex", "opencode"}
	if client == "none" {
		clients = nil
	} else if client != "all" {
		clients = []string{client}
	}
	for _, name := range clients {
		fmt.Fprintln(out)
		switch name {
		case "claude":
			fmt.Fprintf(out, "Claude: add an HTTP MCP entry named %s under mcpServers in ~/.claude.json (or CLAUDE_CONFIG_DIR/.claude.json); set type=http and url=%s. Add headers.Authorization when using a token.\n", mcpClientEntryName, endpoint)
		case "codex":
			fmt.Fprintf(out, "Codex: add [mcp_servers.%s] in $CODEX_HOME/config.toml (default ~/.codex/config.toml); set url=%q and enabled=true. Set http_headers.Authorization when using a token.\n", mcpClientEntryName, endpoint)
		case "opencode":
			fmt.Fprintf(out, "OpenCode: use OPENCODE_CONFIG when set, otherwise your XDG opencode.json[c]. Add a remote MCP entry named %s with url=%s and oauth=false. Version 1 uses mcp.%s with enabled=true; version 2 uses mcp.servers.%s with disabled=false. Add headers.Authorization when using a token.\n", mcpClientEntryName, endpoint, mcpClientEntryName, mcpClientEntryName)
		}
	}
	_, err := fmt.Fprintln(out, "Use your client account's config path overrides when set. You can run setup --client NAME to register an installed client automatically. Setup and this guide do not start the server.")
	return err
}

func mcpShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

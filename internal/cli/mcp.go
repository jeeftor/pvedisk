package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/jeeftor/pvedisk/internal/mcpserver"
	"github.com/spf13/cobra"
)

func mcpCommand(ops operations, configPath *string) *cobra.Command {
	flags := mcpSettings{}
	var settingsPath string
	command := &cobra.Command{
		Use: "mcp", Short: "Serve your VM inspection and mount tools over HTTP MCP",
		Long: "Serve your VM inspection and mount tools at /mcp over plain HTTP. Saved MCP settings are loaded before explicit flag overrides. Run mcp setup to save settings and register installed clients. Starting the server never writes settings, loads site configuration, or runs setup; mount operations require valid site configuration. Mounting, including read-only mounting, may shut down a running VM and keeps it stopped until cleanup succeeds.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			settings, err := loadMCPSettings(settingsPath)
			if err != nil {
				return err
			}
			settings = overrideMCPSettings(cmd, settings, flags)
			if err := settings.validate(); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return ops.mcp(ctx, *configPath, mcpserver.HTTPOptions{Listen: settings.Listen, TokenFile: settings.TokenFile, NoToken: settings.NoToken}, cmd.ErrOrStderr())
		},
	}
	command.PersistentFlags().StringVar(&settingsPath, "mcp-config", "", "MCP settings file (default: $XDG_CONFIG_HOME/pvedisk/mcp.yaml)")
	command.Flags().StringVar(&flags.Listen, "listen", "127.0.0.1:8766", "Override your saved HTTP listen address")
	command.Flags().StringVar(&flags.TokenFile, "token-file", "", "Override your saved private bearer token file")
	command.Flags().BoolVar(&flags.NoToken, "no-token", false, "Override saved authentication with explicit tokenless access")
	command.MarkFlagsMutuallyExclusive("token-file", "no-token")
	command.AddCommand(mcpSetupCommand(&settingsPath, mcpSetupOperations{detect: detectedMCPClients, install: installMCPClient}))
	return command
}

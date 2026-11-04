package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/jeeftor/pvedisk/internal/mcpserver"
	"github.com/spf13/cobra"
)

func mcpCommand(ops operations, configPath *string) *cobra.Command {
	options := mcpserver.HTTPOptions{}
	command := &cobra.Command{
		Use: "mcp", Short: "Serve your VM inspection and mount tools over HTTP MCP",
		Long: "Serve your VM inspection and mount tools at /mcp over plain HTTP. The default listener is loopback-only. A non-loopback listener requires --token-file or explicit --no-token. Starting the server does not load site configuration or run setup; mount operations require valid configuration. Mounting, including read-only mounting, may shut down a running VM and keeps it stopped until cleanup succeeds.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return ops.mcp(ctx, *configPath, options, cmd.ErrOrStderr())
		},
	}
	command.Flags().StringVar(&options.Listen, "listen", "127.0.0.1:8766", "HTTP listen address")
	command.Flags().StringVar(&options.TokenFile, "token-file", "", "Absolute path to your private bearer token file")
	command.Flags().BoolVar(&options.NoToken, "no-token", false, "Explicitly allow clients without a bearer token")
	command.MarkFlagsMutuallyExclusive("token-file", "no-token")
	return command
}

package cli

import (
	"fmt"

	"github.com/jeeftor/pvedisk/internal/buildinfo"
	"github.com/spf13/cobra"
)

func versionCommand() *cobra.Command {
	var short, asJSON bool
	command := &cobra.Command{Use: "version", Short: "Show your pvedisk version and build metadata", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := buildinfo.Current()
			if short {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), info.Version)
				return err
			}
			if asJSON {
				return writeStatus(cmd.OutOrStdout(), info)
			}
			p := outputPalette(cmd.OutOrStdout())
			metadata := fmt.Sprintf("%s %s\n\n%s %s\n%s %s\n%s %s\n%s %s\n%s %s", p.title.Render("pvedisk"), p.success.Render(info.Version), p.label.Render("Commit:"), info.Commit, p.label.Render("Built:"), info.BuildDate, p.label.Render("Source:"), info.Source, p.label.Render("Go:"), info.GoVersion, p.label.Render("Target:"), info.OS+"/"+info.Arch)
			_, err := fmt.Fprintln(cmd.OutOrStdout(), p.panel(metadata, terminalWidth(cmd.OutOrStdout())))
			return err
		},
	}
	command.Flags().BoolVar(&short, "short", false, "Print only your version")
	command.Flags().BoolVar(&asJSON, "json", false, "Print your build metadata as JSON")
	command.MarkFlagsMutuallyExclusive("short", "json")
	return command
}

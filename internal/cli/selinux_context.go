package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
)

func selinuxContextCommand(read func(string) (string, error)) *cobra.Command {
	var path string
	command := &cobra.Command{
		Use: "selinux-context", Short: "Show the existing SELinux label on your mounted guest file",
		Long: "Read the security.selinux extended attribute of one regular file on your mounted guest. Your absolute host path must contain no symbolic links. This read-only helper does not assign labels, check the expected policy label, or report whether your guest enforces SELinux.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsFunc(path, unicode.IsControl) {
				return errors.New("provide a clean absolute mounted-file path with --path")
			}
			label, err := read(path)
			if err != nil {
				return fmt.Errorf("read your file's SELinux context: %w", err)
			}
			label = strings.TrimSuffix(label, "\x00")
			if label == "" || strings.ContainsFunc(label, unicode.IsControl) {
				return errors.New("your file has an empty or malformed SELinux context")
			}
			p := outputPalette(cmd.OutOrStdout())
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n%s %s\n", p.label.Render("File:"), p.path.Render(path), p.label.Render("SELinux context:"), p.value.Render(label))
			return err
		},
	}
	command.Flags().StringVar(&path, "path", "", "Absolute host path to a regular file in your mounted guest")
	return command
}

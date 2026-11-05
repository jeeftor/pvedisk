package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/spf13/cobra"
)

func mountWithCleanupPrompt(cmd *cobra.Command, ops operations, id int, configPath string, readOnly bool, progress func(string)) error {
	err := ops.mount(cmd.Context(), id, configPath, readOnly, progress)
	if !errors.Is(err, session.ErrSessionExists) || !ops.terminal(cmd) {
		return err
	}
	if contextErr := cmd.Context().Err(); contextErr != nil {
		return errors.Join(err, contextErr)
	}
	p := outputPalette(cmd.OutOrStdout())
	if _, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "%s\nCleanup may unmount your existing repair mounts.\n%s ", p.warning.Render(fmt.Sprintf("Your VM %d already has a repair session.", id)), p.label.Render(fmt.Sprintf("Clean up your session for VM %d and retry mounting? [y/N]", id))); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	answer, readErr := bufio.NewReader(io.LimitReader(cmd.InOrStdin(), 64)).ReadString('\n')
	if readErr != nil {
		return err
	}
	answer = strings.TrimSpace(answer)
	if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		return err
	}
	if contextErr := cmd.Context().Err(); contextErr != nil {
		return errors.Join(err, contextErr)
	}
	if cleanErr := ops.clean(cmd.Context(), id); cleanErr != nil {
		return fmt.Errorf("clean up your session for VM %d before mounting; your mount was not retried: %w", id, cleanErr)
	}
	return ops.mount(cmd.Context(), id, configPath, readOnly, progress)
}

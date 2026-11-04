package session

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// busyMountDiagnostic identifies mount users without signaling them or exposing
// process arguments. A failed diagnostic must not delay cleanup indefinitely.
func busyMountDiagnostic(path string) string {
	if _, err := exec.LookPath("fuser"); err != nil {
		quoted := "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
		return "To inspect mount users, install psmisc and run: fuser -vm -- " + quoted
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "fuser", "-vm", "--", path)
	cmd.WaitDelay = 100 * time.Millisecond
	output, err := cmd.CombinedOutput()
	detail := diagnosticOutput(output, 3072)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "Mount users (fuser diagnostic timed out):\n" + detail
	}
	if detail != "" {
		return "Mount users (fuser: USER, PID, ACCESS, COMMAND):\n" + detail
	}
	if err != nil {
		return "fuser could not identify mount users: " + err.Error()
	}
	return "fuser did not report any mount users."
}

func diagnosticOutput(output []byte, limit int) string {
	text := strings.TrimSpace(string(output))
	if len(text) > limit {
		text = strings.ToValidUTF8(text[:limit], "?") + "\n[output truncated]"
	}
	return text
}

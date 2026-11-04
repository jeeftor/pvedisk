package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// busyMountDiagnostic identifies mount users without signaling them or exposing
// process arguments. A failed diagnostic must not delay cleanup indefinitely.
func busyMountDiagnostic(path string) string {
	if _, err := exec.LookPath("fuser"); err != nil {
		return "To inspect mount users, install psmisc and run: fuser -vm -- " + quoteDiagnosticArgument(path)
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
		return "Mount users (fuser: USER, PID, ACCESS, COMMAND):\n" + detail + "\nAccess: c=current directory, r=root directory, f/F=open file, e=executable, m=mapped file.\nLeave the mount in each listed shell (for example, run cd /), and close files in the listed applications before retrying cleanup."
	}
	if err != nil {
		return "fuser could not identify mount users: " + err.Error()
	}
	return "fuser did not report any mount users."
}

func quoteDiagnosticArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func processDescription(procRoot, pid string) string {
	process := pid
	if name, err := os.ReadFile(filepath.Join(procRoot, pid, "comm")); err == nil {
		process += " (" + strconv.Quote(strings.TrimSpace(string(name))) + ")"
	}
	return process
}

func diskUserError(disk, procRoot, pid, fd string) error {
	path := filepath.Join(procRoot, pid, "fd", fd)
	descriptor := path
	if target, err := os.Readlink(path); err == nil {
		descriptor += " -> " + strconv.Quote(target)
	}
	return fmt.Errorf("your disk %s is open in process %s; protection is retained\nDescriptor: %s\nInspect with: readlink -- %s\nClose this disk in the identified process, then retry cleanup", disk, processDescription(procRoot, pid), descriptor, quoteDiagnosticArgument(path))
}

func diagnosticOutput(output []byte, limit int) string {
	text := strings.TrimSpace(string(output))
	if len(text) > limit {
		text = strings.ToValidUTF8(text[:limit], "?") + "\n[output truncated]"
	}
	return text
}

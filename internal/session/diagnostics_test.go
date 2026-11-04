package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func diagnosticTool(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestBusyMountDiagnosticListsPIDAndCommandName(t *testing.T) {
	dir := t.TempDir()
	diagnosticTool(t, dir, "fuser", `[ "$*" = '-vm -- /repair/vm400' ] || exit 2
printf '%s\n' '                     USER        PID ACCESS COMMAND' >&2
printf '%s\n' '/repair/vm400:       root       1234 ..c.. bash' >&2
`)
	t.Setenv("PATH", dir)
	output := busyMountDiagnostic("/repair/vm400")
	for _, want := range []string{"USER", "PID", "COMMAND", "1234", "bash"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q: %s", want, output)
		}
	}
}

func TestBusyMountDiagnosticMissingFuser(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	output := busyMountDiagnostic("/repair/it's here")
	if !strings.Contains(output, "install psmisc") || !strings.Contains(output, "fuser -vm -- '/repair/it'\\''s here'") {
		t.Fatalf("missing useful safely quoted fallback: %s", output)
	}
}

func TestBusyMountDiagnosticTimeout(t *testing.T) {
	dir := t.TempDir()
	diagnosticTool(t, dir, "fuser", "exec /bin/sleep 10\n")
	t.Setenv("PATH", dir)
	start := time.Now()
	output := busyMountDiagnostic("/repair/vm400")
	if time.Since(start) > 5*time.Second {
		t.Fatal("mount diagnostic exceeded deadline")
	}
	if !strings.Contains(output, "timed out") {
		t.Fatalf("missing timeout explanation: %s", output)
	}
}

func TestUnmountReportsFailureAndMountUsers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux mountinfo")
	}
	dir := t.TempDir()
	diagnosticTool(t, dir, "umount", "printf '%s\\n' 'umount: target is busy' >&2\nexit 1\n")
	diagnosticTool(t, dir, "fuser", "printf '%s\\n' 'root 5678 ..c.. bash' >&2\n")
	t.Setenv("PATH", dir)
	// The executable is the fake script above; the real root mount is never changed.
	err := unmount("/")
	if err == nil {
		t.Fatal("failed unmount accepted")
	}
	for _, want := range []string{"target is busy", "5678", "bash", "protection is retained"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q: %v", want, err)
		}
	}
}

func TestMountDiagnosticOutputBounded(t *testing.T) {
	output := diagnosticOutput([]byte(strings.Repeat("x", 10000)), 1024)
	if len(output) > 1100 || !strings.Contains(output, "truncated") {
		t.Fatal("diagnostic output is not bounded")
	}
}

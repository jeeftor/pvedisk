//go:build linux && cgo

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual command wiring and libcrypt together for redirected input.
func TestNativeUtilityAutomationRoundTrip(t *testing.T) {
	const candidate = "  known%password!  "
	run := func(input string, args ...string) (string, string, error) {
		var output, diagnostics bytes.Buffer
		command := NewCommand()
		command.SetArgs(args)
		command.SetIn(strings.NewReader(input))
		command.SetOut(&output)
		command.SetErr(&diagnostics)
		err := command.Execute()
		return output.String(), diagnostics.String(), err
	}
	output, diagnostics, err := run(candidate+"\n", "utils", "hash-password")
	if err != nil || diagnostics != "" || !strings.HasPrefix(output, "$y$") || strings.Contains(output, candidate) {
		t.Fatalf("hash utility failed or produced unexpected diagnostics: %v", err)
	}
	encoded := strings.TrimSuffix(output, "\n")
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(directory, "shadow")
	if err := os.WriteFile(shadow, []byte("root:"+encoded+":20000:0:99999:7:::\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		input string
		match bool
	}{{candidate + "\r\n", true}, {"incorrect candidate\n", false}} {
		output, diagnostics, err := run(test.input, "utils", "verify-password", "--shadow", shadow)
		if (err == nil) != test.match || diagnostics != "" {
			t.Fatalf("verification match=%v error=%v", test.match, err)
		}
		if strings.Contains(output, encoded) || strings.Contains(output, candidate) || strings.Contains(output, "incorrect candidate") {
			t.Fatal("verification exposed a password or hash")
		}
		if test.match && !strings.Contains(output, "matches") {
			t.Fatal("verification did not confirm the matching candidate")
		}
	}
}

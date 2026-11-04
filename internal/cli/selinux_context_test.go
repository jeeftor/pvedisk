package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSELinuxContextReadOnlyOutputAndValidation(t *testing.T) {
	for _, test := range []struct {
		name, path, label string
		readError         error
		wantError         bool
	}{
		{name: "label", path: "/vault/vm407/etc/shadow", label: "system_u:object_r:shadow_t:s0\x00"},
		{name: "relative", path: "etc/shadow", wantError: true},
		{name: "unclean", path: "/vault/../etc/shadow", wantError: true},
		{name: "control-path", path: "/vault/vm407/\nfile", wantError: true},
		{name: "missing", wantError: true},
		{name: "read-error", path: "/vault/vm407/etc/shadow", readError: errors.New("no security.selinux label"), wantError: true},
		{name: "malformed", path: "/vault/vm407/etc/shadow", label: "context\nforged output", wantError: true},
		{name: "empty", path: "/vault/vm407/etc/shadow", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			command := selinuxContextCommand(func(path string) (string, error) {
				calls++
				if path != test.path {
					t.Fatalf("path=%q", path)
				}
				return test.label, test.readError
			})
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&output)
			command.SilenceErrors, command.SilenceUsage = true, true
			command.SetArgs([]string{"--path", test.path})
			err := command.Execute()
			if (err != nil) != test.wantError {
				t.Fatalf("output=%q error=%v", output.String(), err)
			}
			if test.name == "relative" || test.name == "unclean" || test.name == "missing" || test.name == "control-path" {
				if calls != 0 {
					t.Fatal("read invalid path")
				}
			} else if calls != 1 {
				t.Fatalf("reads=%d", calls)
			}
			if test.wantError && output.Len() != 0 {
				t.Fatalf("unexpected output=%q", output.String())
			}
			if !test.wantError && (!strings.Contains(output.String(), test.path) || !strings.Contains(output.String(), "SELinux context: system_u:object_r:shadow_t:s0") || strings.ContainsRune(output.String(), 0)) {
				t.Fatalf("output=%q", output.String())
			}
		})
	}
}

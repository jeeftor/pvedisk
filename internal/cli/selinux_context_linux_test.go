//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSELinuxContextRejectsSymlinksAndReportsMissingLabels(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "guest-file")
	if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readSELinuxContext(link); err == nil {
		t.Fatal("accepted symlink")
	}
	label, err := readSELinuxContext(file)
	// SELinux-enabled test hosts can assign a real label automatically.
	if err != nil && !strings.Contains(err.Error(), "no security.selinux label") && !strings.Contains(err.Error(), "does not expose") {
		t.Fatalf("label=%q error=%v", label, err)
	}
	after, err := os.ReadFile(file)
	if err != nil || string(after) != "unchanged" {
		t.Fatal("inspection changed file")
	}
}

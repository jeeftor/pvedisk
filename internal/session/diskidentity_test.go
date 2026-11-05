package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/proxmox"
)

func TestDiskIdentitySurvivesHardLinkAndDetectsReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "disk.img")
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	identities, err := captureDiskIdentities([]proxmox.Disk{{Path: path, Format: "raw"}})
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.img")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(alias)
	if err != nil || !matchesDiskIdentity(info, identities[path]) {
		t.Fatal("hard link lost disk identity")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("different image"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if matchesDiskIdentity(info, identities[path]) {
		t.Fatal("replacement accepted as original disk")
	}
}

func TestLegacyMissingDiskRetainsSession(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := m.intent(400, root, false)
	if err != nil {
		t.Fatal(err)
	}
	s.Disks = []proxmox.Disk{{Path: filepath.Join(root, "missing.img"), Format: "raw"}}
	s.ProtectionAttempted = true
	if err = m.save(s); err != nil {
		t.Fatal(err)
	}
	err = assertCleanupDisksUnusedAt(s, filepath.Join(root, "sys"), filepath.Join(root, "proc"))
	if err == nil || !strings.Contains(err.Error(), "no recorded disk identity") {
		t.Fatalf("legacy missing path error=%v", err)
	}
	if _, err = m.Status(400); err != nil {
		t.Fatal("uncertain cleanup lost persistent state")
	}
}

func TestDiskIdentityDescriptorScanChecksAliases(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "disk.img")
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	identities, err := captureDiskIdentities([]proxmox.Disk{{Path: path, Format: "raw"}})
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.img")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(root, "proc")
	fds := filepath.Join(proc, "123", "fd")
	if err := os.MkdirAll(fds, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alias, filepath.Join(fds, "7")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "123", "comm"), []byte("qemu\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "123", "cmdline"), []byte("qemu\x00--password\x00do-not-print-this\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	err = assertDiskIdentitiesUnusedAt(identities, proc)
	if err == nil || !strings.Contains(err.Error(), "123 (\"qemu\")") {
		t.Fatalf("open alias not identified: %v", err)
	}
	for _, want := range []string{alias, filepath.Join(fds, "7"), "readlink --", "Close this disk"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing blocker/action %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "do-not-print-this") {
		t.Fatal("process arguments exposed")
	}
	if err := os.Remove(filepath.Join(fds, "7")); err != nil {
		t.Fatal(err)
	}
	if err := assertDiskIdentitiesUnusedAt(identities, proc); err != nil {
		t.Fatal(err)
	}
}

func TestDiskIdentityStateRoundTrip(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "disk.img")
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := m.intent(400, root, false)
	if err != nil {
		t.Fatal(err)
	}
	s.Disks = []proxmox.Disk{{Path: path, Format: "raw"}}
	s.DiskIdentities, err = captureDiskIdentities(s.Disks)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.save(s); err != nil {
		t.Fatal(err)
	}
	stored, err := m.Status(400)
	if err != nil || stored.DiskIdentities[path] != s.DiskIdentities[path] {
		t.Fatal("disk identity not persisted")
	}
}

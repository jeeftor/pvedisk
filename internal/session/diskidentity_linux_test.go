//go:build linux

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/proxmox"
	"golang.org/x/sys/unix"
)

func cleanupDiskFixture(t *testing.T) (*State, string, string) {
	t.Helper()
	root := t.TempDir()
	sysRoot, procRoot := filepath.Join(root, "sys"), filepath.Join(root, "proc")
	if err := os.MkdirAll(filepath.Join(sysRoot, "class", "block"), 0700); err != nil {
		t.Fatal(err)
	}
	writeKernelFixture(t, filepath.Join(procRoot, "self", "mountinfo"), "1 0 8:0 / / rw - ext4 /dev/sda rw\n")
	writeKernelFixture(t, filepath.Join(procRoot, "swaps"), "Filename Type Size Used Priority\n")
	path := filepath.Join(root, "disk.img")
	writeKernelFixture(t, path, "image")
	s := &State{VMID: 400, Disks: []proxmox.Disk{{Path: path, Format: "raw"}}}
	var err error
	s.DiskIdentities, err = captureDiskIdentities(s.Disks)
	if err != nil {
		t.Fatal(err)
	}
	return s, sysRoot, procRoot
}

func TestCleanupMissingImageChecksUnlinkedDescriptor(t *testing.T) {
	s, sysRoot, procRoot := cleanupDiskFixture(t)
	path := s.Disks[0].Path
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fdPath := filepath.Join(procRoot, "456", "fd", "9")
	if err := os.MkdirAll(filepath.Dir(fdPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fmt.Sprintf("/proc/self/fd/%d", f.Fd()), fdPath); err != nil {
		t.Fatal(err)
	}
	err = assertCleanupDisksUnusedAt(s, sysRoot, procRoot)
	if err == nil || !strings.Contains(err.Error(), "open in process 456") {
		t.Fatalf("unlinked descriptor ignored: %v", err)
	}
	if err := os.Remove(fdPath); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err != nil {
		t.Fatalf("deleted unused disk rejected: %v", err)
	}
}

func TestCleanupMissingImageChecksLoopAlias(t *testing.T) {
	s, sysRoot, procRoot := cleanupDiskFixture(t)
	path := s.Disks[0].Path
	alias := path + ".alias"
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(sysRoot, "class", "block", "loop0")
	writeKernelFixture(t, filepath.Join(block, "dev"), "7:0\n")
	if err := os.Mkdir(filepath.Join(block, "holders"), 0700); err != nil {
		t.Fatal(err)
	}
	writeKernelFixture(t, filepath.Join(block, "loop", "backing_file"), alias+"\n")
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "active loop mapping") {
		t.Fatalf("active loop alias ignored: %v", err)
	}
}

func TestCleanupMissingBlockChecksKernelIdentity(t *testing.T) {
	s, sysRoot, procRoot := cleanupDiskFixture(t)
	path := s.Disks[0].Path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.DiskIdentities[path] = DiskIdentity{Kind: "block", Device: 1, Inode: 100, BlockDevice: unix.Mkdev(253, 17), Boot: s.DiskIdentities[path].Boot}
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err != nil {
		t.Fatalf("deleted unused block rejected: %v", err)
	}
	writeKernelFixture(t, filepath.Join(procRoot, "self", "mountinfo"), "1 0 253:17 / /mounted rw - ext4 /dev/dm-17 rw\n")
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "mounted or active swap") {
		t.Fatalf("mounted block alias ignored: %v", err)
	}
}

func TestCleanupReusedPathChecksOriginalDiskOnly(t *testing.T) {
	s, sysRoot, procRoot := cleanupDiskFixture(t)
	path := s.Disks[0].Path
	originalIdentity := s.DiskIdentities[path]
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	writeKernelFixture(t, path, "different image")
	fd := filepath.Join(procRoot, "123", "fd", "7")
	if err := os.MkdirAll(filepath.Dir(fd), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, fd); err != nil {
		t.Fatal(err)
	}
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "(replacement) is open in process 123") {
		t.Fatalf("open replacement disk ignored: %v", err)
	}
	if s.DiskIdentities[path] != originalIdentity {
		t.Fatal("cleanup adopted replacement disk identity")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "different image" {
		t.Fatal("replacement disk modified")
	}
	if err := os.Remove(fd); err != nil {
		t.Fatal(err)
	}
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err != nil {
		t.Fatalf("unused original and replacement rejected: %v", err)
	}
	if err := os.Symlink(path+".original", fd); err != nil {
		t.Fatal(err)
	}
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "open in process 123") {
		t.Fatalf("open original disk ignored: %v", err)
	}
}

func TestReusedPathChecksReplacementLoopMapping(t *testing.T) {
	s, sysRoot, procRoot := cleanupDiskFixture(t)
	path := s.Disks[0].Path
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	writeKernelFixture(t, path, "replacement")
	block := filepath.Join(sysRoot, "class", "block", "loop0")
	writeKernelFixture(t, filepath.Join(block, "dev"), "7:0\n")
	if err := os.Mkdir(filepath.Join(block, "holders"), 0700); err != nil {
		t.Fatal(err)
	}
	writeKernelFixture(t, filepath.Join(block, "loop", "backing_file"), path+"\n")
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "(replacement) has active loop mapping") {
		t.Fatalf("replacement loop mapping ignored: %v", err)
	}
}

func TestReusedPathRequiresOriginalBootIdentity(t *testing.T) {
	for _, boot := range []string{"", "older-host-boot"} {
		t.Run(boot, func(t *testing.T) {
			s, sysRoot, procRoot := cleanupDiskFixture(t)
			path := s.Disks[0].Path
			if err := os.Rename(path, path+".original"); err != nil {
				t.Fatal(err)
			}
			writeKernelFixture(t, path, "new image")
			identity := s.DiskIdentities[path]
			identity.Boot = boot
			s.DiskIdentities[path] = identity
			reason := "no host boot identifier"
			if boot != "" {
				reason = "predates the current host boot"
			}
			if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), reason) || !strings.Contains(err.Error(), "replacement disk") || !strings.Contains(err.Error(), path) {
				t.Fatalf("uncertain original identity accepted: %v", err)
			}
		})
	}
}

func TestReusedPathChecksOriginalBlockMounts(t *testing.T) {
	s, sysRoot, procRoot := cleanupDiskFixture(t)
	path := s.Disks[0].Path
	s.DiskIdentities[path] = DiskIdentity{Kind: "block", Device: 1, Inode: 100, BlockDevice: unix.Mkdev(253, 17), Boot: s.DiskIdentities[path].Boot}
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err != nil {
		t.Fatalf("unused original block identity rejected: %v", err)
	}
	writeKernelFixture(t, filepath.Join(procRoot, "self", "mountinfo"), "1 0 253:17 / /mounted rw - ext4 /dev/dm-17 rw\n")
	if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "mounted or active swap") {
		t.Fatalf("mounted original block identity ignored: %v", err)
	}
}

func TestMissingDiskIdentityFromAnotherBootRetainsProtection(t *testing.T) {
	for _, kind := range []string{"file", "block"} {
		t.Run(kind, func(t *testing.T) {
			s, sysRoot, procRoot := cleanupDiskFixture(t)
			path := s.Disks[0].Path
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			identity := s.DiskIdentities[path]
			identity.Kind = kind
			identity.Boot = "older-host-boot"
			if kind == "block" {
				identity.BlockDevice = unix.Mkdev(253, 17)
			}
			s.DiskIdentities[path] = identity
			if err := assertCleanupDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "host reboot") {
				t.Fatalf("identity reused across reboot: %v", err)
			}
		})
	}
}

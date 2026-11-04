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

func recoveryDiskFixture(t *testing.T) (*State, string, string) {
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

func TestRecoveryMissingImageChecksUnlinkedDescriptor(t *testing.T) {
	s, sysRoot, procRoot := recoveryDiskFixture(t)
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
	err = assertRecoveryDisksUnusedAt(s, sysRoot, procRoot)
	if err == nil || !strings.Contains(err.Error(), "open in process 456") {
		t.Fatalf("unlinked descriptor ignored: %v", err)
	}
	if err := os.Remove(fdPath); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := assertRecoveryDisksUnusedAt(s, sysRoot, procRoot); err != nil {
		t.Fatalf("deleted unused disk rejected: %v", err)
	}
}

func TestRecoveryMissingImageChecksLoopAlias(t *testing.T) {
	s, sysRoot, procRoot := recoveryDiskFixture(t)
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
	if err := assertRecoveryDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "active loop mapping") {
		t.Fatalf("active loop alias ignored: %v", err)
	}
}

func TestRecoveryMissingBlockChecksKernelIdentity(t *testing.T) {
	s, sysRoot, procRoot := recoveryDiskFixture(t)
	path := s.Disks[0].Path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.DiskIdentities[path] = DiskIdentity{Kind: "block", Device: 1, Inode: 100, BlockDevice: unix.Mkdev(253, 17), Boot: s.DiskIdentities[path].Boot}
	if err := assertRecoveryDisksUnusedAt(s, sysRoot, procRoot); err != nil {
		t.Fatalf("deleted unused block rejected: %v", err)
	}
	writeKernelFixture(t, filepath.Join(procRoot, "self", "mountinfo"), "1 0 253:17 / /mounted rw - ext4 /dev/dm-17 rw\n")
	if err := assertRecoveryDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "mounted or active swap") {
		t.Fatalf("mounted block alias ignored: %v", err)
	}
}

func TestRecoveryRejectsReusedDiskPath(t *testing.T) {
	s, sysRoot, procRoot := recoveryDiskFixture(t)
	path := s.Disks[0].Path
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	writeKernelFixture(t, path, "different image")
	if err := assertRecoveryDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "different disk") {
		t.Fatalf("reused path accepted: %v", err)
	}
}

func TestMissingDiskIdentityFromAnotherBootRetainsProtection(t *testing.T) {
	for _, kind := range []string{"file", "block"} {
		t.Run(kind, func(t *testing.T) {
			s, sysRoot, procRoot := recoveryDiskFixture(t)
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
			if err := assertRecoveryDisksUnusedAt(s, sysRoot, procRoot); err == nil || !strings.Contains(err.Error(), "host reboot") {
				t.Fatalf("identity reused across reboot: %v", err)
			}
		})
	}
}

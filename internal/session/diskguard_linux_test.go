//go:build linux

package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeeftor/pvedisk/internal/proxmox"
)

func TestKernelBlockMountsAndHolders(t *testing.T) {
	blocks := []kernelBlock{
		{name: "sda", number: "8:0", location: "/devices/sda"},
		{name: "sda1", number: "8:1", location: "/devices/sda/sda1"},
		{name: "dm-0", number: "253:0", location: "/devices/virtual/dm-0"},
		{name: "dm-1", number: "253:1", location: "/devices/virtual/dm-1"},
	}
	if err := checkKernelBlock("8:0", blocks, map[string]bool{"8:1": true}); err == nil {
		t.Fatal("accepted disk with host-mounted partition")
	}
	if err := checkKernelBlock("8:1", blocks, map[string]bool{"8:1": true}); err == nil {
		t.Fatal("accepted host-mounted target partition")
	}
	if err := checkKernelBlock("253:1", blocks, map[string]bool{"253:0": true, "8:1": true}); err != nil {
		t.Fatalf("rejected independent sibling logical volume: %v", err)
	}
	blocks[1].holders = []string{"dm-0"}
	if err := checkKernelBlock("8:0", blocks, nil); err == nil {
		t.Fatal("accepted disk with active partition holder")
	}
	if err := checkKernelBlock("8:1", blocks, nil); err == nil {
		t.Fatal("accepted partition with active holder")
	}
	if err := checkKernelBlock("99:0", blocks, nil); err == nil {
		t.Fatal("accepted absent block topology")
	}
}

func writeKernelFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestKernelImageLoopMapping(t *testing.T) {
	base := t.TempDir()
	sysRoot, procRoot := filepath.Join(base, "sys"), filepath.Join(base, "proc")
	image := filepath.Join(base, "disk.img")
	writeKernelFixture(t, image, "image")
	writeKernelFixture(t, filepath.Join(procRoot, "self", "mountinfo"), "1 0 8:0 / / rw - ext4 /dev/sda rw\n")
	writeKernelFixture(t, filepath.Join(procRoot, "swaps"), "Filename Type Size Used Priority\n")
	block := filepath.Join(sysRoot, "class", "block", "loop0")
	writeKernelFixture(t, filepath.Join(block, "dev"), "7:0\n")
	if err := os.Mkdir(filepath.Join(block, "holders"), 0700); err != nil {
		t.Fatal(err)
	}
	disks := []proxmox.Disk{{Path: image, Format: "raw"}}
	if err := kernelDisksUnusedAt(disks, sysRoot, procRoot); err != nil {
		t.Fatalf("unused image rejected: %v", err)
	}
	alias := filepath.Join(base, "alias.img")
	if err := os.Link(image, alias); err != nil {
		t.Fatal(err)
	}
	writeKernelFixture(t, filepath.Join(block, "loop", "backing_file"), alias+"\n")
	if err := kernelDisksUnusedAt(disks, sysRoot, procRoot); err == nil {
		t.Fatal("accepted image with active loop mapping through hard link")
	}
	if err := os.Remove(filepath.Join(block, "loop", "backing_file")); err != nil {
		t.Fatal(err)
	}
	writeKernelFixture(t, filepath.Join(procRoot, "swaps"), "Filename Type Size Used Priority\n"+image+" file 1 0 -2\n")
	if err := kernelDisksUnusedAt(disks, sysRoot, procRoot); err == nil {
		t.Fatal("accepted image used as active swap")
	}
}

func TestKernelMountNamespaces(t *testing.T) {
	procRoot := t.TempDir()
	writeKernelFixture(t, filepath.Join(procRoot, "self", "mountinfo"), "1 0 8:0 / / rw - ext4 /dev/sda rw\n")
	writeKernelFixture(t, filepath.Join(procRoot, "123", "mountinfo"), "2 0 8:1 / /private rw - ext4 /dev/sda1 rw\n")
	if err := os.Mkdir(filepath.Join(procRoot, "456"), 0700); err != nil {
		t.Fatal(err)
	}
	mounts, err := readKernelMounts(procRoot)
	if err != nil || !mounts["8:0"] || !mounts["8:1"] {
		t.Fatalf("failed to reconcile mount namespaces: %v, %v", mounts, err)
	}
	writeKernelFixture(t, filepath.Join(procRoot, "123", "mountinfo"), "malformed\n")
	if _, err := readKernelMounts(procRoot); err == nil {
		t.Fatal("accepted malformed namespace information")
	}
}

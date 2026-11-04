//go:build linux

package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jeeftor/pvedisk/internal/proxmox"
	"golang.org/x/sys/unix"
)

// kernelDisksUnused supplements descriptor scans with kernel-owned mounts and
// mappings, which do not need an open descriptor in a userspace process.
func kernelDisksUnused(disks []proxmox.Disk) error {
	return kernelDisksUnusedAt(disks, "/sys", "/proc")
}

type kernelBlock struct {
	name, number, location string
	holders                []string
	backing                string
}

func kernelDisksUnusedAt(disks []proxmox.Disk, sysRoot, procRoot string) error {
	blocks, err := readKernelBlocks(sysRoot)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(procRoot, "self", "mountinfo"))
	if err != nil {
		return fmt.Errorf("inspect kernel mounts: %w", err)
	}
	mounted := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 7 || !strings.Contains(line, " - ") {
			return errors.New("cannot reconcile malformed kernel mount information")
		}
		mounted[fields[2]] = true
	}
	for _, disk := range disks {
		info, err := os.Stat(disk.Path)
		if err != nil {
			return fmt.Errorf("inspect your disk %s: %w", disk.Path, err)
		}
		if info.Mode().IsRegular() {
			for _, block := range blocks {
				if block.backing == "" {
					continue
				}
				backing, err := os.Stat(block.backing)
				if err != nil {
					return fmt.Errorf("cannot reconcile loop backing file %s: %w", block.backing, err)
				}
				if os.SameFile(info, backing) {
					return fmt.Errorf("your disk %s has active loop mapping %s; protection is retained", disk.Path, block.name)
				}
			}
			continue
		}
		if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
			return fmt.Errorf("your disk %s is not a regular image or block device", disk.Path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot identify your block device %s", disk.Path)
		}
		number := fmt.Sprintf("%d:%d", unix.Major(uint64(stat.Rdev)), unix.Minor(uint64(stat.Rdev)))
		if err := checkKernelBlock(number, blocks, mounted); err != nil {
			return fmt.Errorf("your disk %s: %w", disk.Path, err)
		}
	}
	return nil
}

func checkKernelBlock(number string, blocks []kernelBlock, mounted map[string]bool) error {
	var location string
	for _, block := range blocks {
		if block.number == number {
			location = block.location
			break
		}
	}
	if location == "" {
		return fmt.Errorf("block device %s is absent from kernel topology; protection is retained", number)
	}
	for _, block := range blocks {
		if block.location != location && !strings.HasPrefix(block.location, location+string(filepath.Separator)) {
			continue
		}
		if mounted[block.number] {
			return fmt.Errorf("block device %s is mounted by your host; protection is retained", block.name)
		}
		if len(block.holders) != 0 {
			return fmt.Errorf("block device %s has active holders %s; protection is retained", block.name, strings.Join(block.holders, ", "))
		}
	}
	return nil
}

func readKernelBlocks(sysRoot string) ([]kernelBlock, error) {
	root := filepath.Join(sysRoot, "class", "block")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("inspect kernel block devices: %w", err)
	}
	var blocks []kernelBlock
	for _, entry := range entries {
		base := filepath.Join(root, entry.Name())
		location, err := filepath.EvalSymlinks(base)
		if err != nil {
			return nil, fmt.Errorf("resolve kernel device %s: %w", entry.Name(), err)
		}
		number, err := os.ReadFile(filepath.Join(base, "dev"))
		if err != nil {
			return nil, fmt.Errorf("identify kernel device %s: %w", entry.Name(), err)
		}
		holders, err := os.ReadDir(filepath.Join(base, "holders"))
		if err != nil {
			return nil, fmt.Errorf("inspect kernel device holders %s: %w", entry.Name(), err)
		}
		block := kernelBlock{name: entry.Name(), number: strings.TrimSpace(string(number)), location: location}
		for _, holder := range holders {
			block.holders = append(block.holders, holder.Name())
		}
		backing, err := os.ReadFile(filepath.Join(base, "loop", "backing_file"))
		if err == nil {
			block.backing = strings.TrimSuffix(string(backing), "\n")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect loop mapping %s: %w", entry.Name(), err)
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

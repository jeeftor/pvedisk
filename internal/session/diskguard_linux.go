//go:build linux

package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// kernelRecordedDiskUnusedAt checks the preserved identity even when the disk
// path has disappeared. Missing paths alone never prove a disk is unused.
func kernelRecordedDiskUnusedAt(path string, identity DiskIdentity, sysRoot, procRoot string) error {
	blocks, err := readKernelBlocks(sysRoot)
	if err != nil {
		return err
	}
	mounted, err := readKernelMounts(procRoot)
	if err != nil {
		return err
	}
	swaps, err := readKernelSwaps(procRoot, mounted)
	if err != nil {
		return err
	}
	if identity.Kind == "block" {
		number := fmt.Sprintf("%d:%d", unix.Major(identity.BlockDevice), unix.Minor(identity.BlockDevice))
		if mounted[number] {
			return fmt.Errorf("your recorded disk %s is mounted or active swap on your host; protection is retained", path)
		}
		for _, block := range blocks {
			if block.number == number {
				if err := checkKernelBlock(number, blocks, mounted); err != nil {
					return fmt.Errorf("your recorded disk %s: %w", path, err)
				}
				break
			}
		}
		return nil
	}
	for _, swap := range swaps {
		if matchesDiskIdentity(swap, identity) {
			return fmt.Errorf("your recorded disk %s is active swap; protection is retained", path)
		}
	}
	for _, block := range blocks {
		if block.backing == "" {
			continue
		}
		info, err := os.Stat(block.backing)
		if err != nil {
			return fmt.Errorf("cannot reconcile loop backing file %s: %w", block.backing, err)
		}
		if matchesDiskIdentity(info, identity) {
			return fmt.Errorf("your recorded disk %s has active loop mapping %s; protection is retained", path, block.name)
		}
	}
	return nil
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
	mounted, err := readKernelMounts(procRoot)
	if err != nil {
		return err
	}
	swaps, err := readKernelSwaps(procRoot, mounted)
	if err != nil {
		return err
	}
	for _, disk := range disks {
		info, err := os.Stat(disk.Path)
		if err != nil {
			return fmt.Errorf("inspect your disk %s: %w", disk.Path, err)
		}
		if info.Mode().IsRegular() {
			for _, swap := range swaps {
				if os.SameFile(info, swap) {
					return fmt.Errorf("your disk %s is active swap; protection is retained", disk.Path)
				}
			}
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

func readKernelMounts(procRoot string) (map[string]bool, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect process mount namespaces: %w", err)
	}
	paths := []string{filepath.Join(procRoot, "self", "mountinfo")}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err == nil {
			paths = append(paths, filepath.Join(procRoot, entry.Name(), "mountinfo"))
		}
	}
	mounted := make(map[string]bool)
	for index, path := range paths {
		data, err := os.ReadFile(path)
		if index > 0 && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect kernel mount namespace: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 7 || !strings.Contains(line, " - ") {
				return nil, errors.New("cannot reconcile malformed kernel mount information")
			}
			mounted[fields[2]] = true
		}
	}
	return mounted, nil
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
			return fmt.Errorf("block device %s is mounted or active swap on your host; protection is retained", block.name)
		}
		if len(block.holders) != 0 {
			return fmt.Errorf("block device %s has active holders %s; protection is retained", block.name, strings.Join(block.holders, ", "))
		}
	}
	return nil
}

func readKernelSwaps(procRoot string, occupied map[string]bool) ([]os.FileInfo, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, "swaps"))
	if err != nil {
		return nil, fmt.Errorf("inspect active swap: %w", err)
	}
	var files []os.FileInfo
	for index, line := range strings.Split(string(data), "\n") {
		if index == 0 || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 5 {
			return nil, errors.New("cannot reconcile malformed active swap information")
		}
		info, err := os.Stat(decodeMount(fields[0]))
		if err != nil {
			return nil, fmt.Errorf("inspect active swap device: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, errors.New("cannot identify active swap device")
		}
		device := uint64(stat.Dev)
		if info.Mode()&os.ModeDevice != 0 {
			device = uint64(stat.Rdev)
		}
		occupied[fmt.Sprintf("%d:%d", unix.Major(device), unix.Minor(device))] = true
		files = append(files, info)
	}
	return files, nil
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

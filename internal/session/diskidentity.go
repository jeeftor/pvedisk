package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/jeeftor/pvedisk/internal/proxmox"
)

// DiskIdentity preserves the identity of a disk even if its path is later
// deleted. Block device numbers also identify aliases such as zvol device nodes.
type DiskIdentity struct {
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
	BlockDevice uint64 `json:"block_device,omitempty"`
	Kind        string `json:"kind"`
	Boot        string `json:"boot,omitempty"`
}

func diskIdentity(info os.FileInfo) (DiskIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return DiskIdentity{}, errors.New("cannot identify disk inode")
	}
	identity := DiskIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Kind: "file"}
	if info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0 {
		identity.Kind = "block"
		identity.BlockDevice = uint64(stat.Rdev)
	} else if !info.Mode().IsRegular() {
		return DiskIdentity{}, errors.New("disk is not a regular image or block device")
	}
	return identity, nil
}

func captureDiskIdentities(disks []proxmox.Disk) (map[string]DiskIdentity, error) {
	boot := ""
	if runtime.GOOS == "linux" {
		var err error
		boot, err = diskBootID()
		if err != nil {
			return nil, err
		}
	}
	identities := make(map[string]DiskIdentity, len(disks))
	for _, disk := range disks {
		info, err := os.Stat(disk.Path)
		if err != nil {
			return nil, fmt.Errorf("identify your disk %s: %w", disk.Path, err)
		}
		identity, err := diskIdentity(info)
		if err != nil {
			return nil, err
		}
		identity.Boot = boot
		identities[disk.Path] = identity
	}
	return identities, nil
}

func matchesDiskIdentity(info os.FileInfo, identity DiskIdentity) bool {
	actual, err := diskIdentity(info)
	if err != nil || actual.Kind != identity.Kind {
		return false
	}
	if identity.Kind == "block" {
		return actual.BlockDevice == identity.BlockDevice
	}
	return actual.Device == identity.Device && actual.Inode == identity.Inode
}

func assertRecoveryDisksUnused(s *State) error {
	return assertRecoveryDisksUnusedAt(s, "/sys", "/proc")
}

func assertRecoveryDisksUnusedAt(s *State, sysRoot, procRoot string) error {
	identities := make(map[string]DiskIdentity, len(s.Disks))
	replacements := make(map[string]DiskIdentity)
	for _, disk := range s.Disks {
		if recorded, ok := s.DiskIdentities[disk.Path]; ok {
			identities[disk.Path] = recorded
		}
		info, err := os.Stat(disk.Path)
		if errors.Is(err, os.ErrNotExist) {
			if _, ok := identities[disk.Path]; !ok {
				return fmt.Errorf("cannot verify your missing disk %s: this older session has no recorded disk identity; restore access to the original disk or storage, then retry pvedisk recover %d; protection is retained", disk.Path, s.VMID)
			}
			if err := requireOriginalDiskBoot(disk.Path, identities[disk.Path]); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect your disk %s: %w", disk.Path, err)
		}
		current, err := diskIdentity(info)
		if err != nil {
			return err
		}
		if recorded, ok := identities[disk.Path]; ok && (recorded.Device != current.Device || recorded.Inode != current.Inode || recorded.BlockDevice != current.BlockDevice || recorded.Kind != current.Kind) {
			if err := requireOriginalDiskBoot(disk.Path, recorded); err != nil {
				return fmt.Errorf("your disk path now identifies a replacement disk: %w", err)
			}
			// Keep the original saved identity authoritative when a rebuilt VM
			// reuses its pathname, and check replacement users before unlocking.
			replacements[disk.Path+" (replacement)"] = current
			continue
		}
		identities[disk.Path] = current
	}
	if len(identities) == 0 {
		return nil
	}
	for path, identity := range identities {
		if err := kernelRecordedDiskUnusedAt(path, identity, sysRoot, procRoot); err != nil {
			return err
		}
	}
	for path, identity := range replacements {
		if err := kernelRecordedDiskUnusedAt(path, identity, sysRoot, procRoot); err != nil {
			return err
		}
	}
	if err := assertDiskIdentitiesUnusedAt(identities, procRoot); err != nil {
		return err
	}
	return assertDiskIdentitiesUnusedAt(replacements, procRoot)
}

func requireOriginalDiskBoot(path string, identity DiskIdentity) error {
	boot, err := diskBootID()
	if err != nil {
		return err
	}
	if identity.Boot == "" {
		return fmt.Errorf("cannot verify your original disk %s: its saved identity has no host boot identifier, so same-boot ownership cannot be proven; restore access to the original disk or storage before recovery; protection is retained", path)
	}
	if identity.Boot != boot {
		return fmt.Errorf("cannot verify your original disk %s: its saved identity predates the current host boot, and device or inode numbers may have changed after a host reboot; restore access to the original disk or storage before recovery; protection is retained", path)
	}
	return nil
}

func diskBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("verify your recorded disk's host boot identity: %w", err)
	}
	boot := strings.TrimSpace(string(data))
	if boot == "" {
		return "", errors.New("your host boot identity is empty; protection is retained")
	}
	return boot, nil
}

func assertDiskIdentitiesUnusedAt(identities map[string]DiskIdentity, procRoot string) error {
	if len(identities) == 0 {
		return nil
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(procRoot, entry.Name(), "fd"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot reconcile process disk ownership: %w", err)
		}
		for _, fd := range fds {
			info, err := os.Stat(filepath.Join(procRoot, entry.Name(), "fd", fd.Name()))
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESTALE) {
				continue
			}
			if err != nil {
				return fmt.Errorf("cannot reconcile an open descriptor: %w", err)
			}
			for path, identity := range identities {
				if matchesDiskIdentity(info, identity) {
					return diskUserError(path, procRoot, entry.Name(), fd.Name())
				}
			}
		}
	}
	return nil
}

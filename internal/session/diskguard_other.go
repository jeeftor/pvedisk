//go:build !linux

package session

import (
	"errors"

	"github.com/jeeftor/pvedisk/internal/proxmox"
)

func kernelDisksUnused(_ []proxmox.Disk) error {
	return errors.New("kernel disk ownership checks require Linux")
}

func kernelRecordedDiskUnusedAt(_ string, _ DiskIdentity, _, _ string) error {
	return errors.New("kernel disk ownership checks require Linux")
}

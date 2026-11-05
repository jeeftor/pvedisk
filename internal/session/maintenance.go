package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/proxmox"
)

type maintenanceProtection interface {
	Inspect(context.Context, int) ([]proxmox.Disk, error)
	Shutdown(context.Context, int) error
	Acquire(context.Context, int, string) ([]proxmox.Disk, error)
	Release(context.Context, int, string) error
}

func (m *Manager) offlineMaintenance(ctx context.Context, id int, configPath, phase string, operation func(*State) error) error {
	return m.lockedContext(ctx, func() error {
		if _, err := config.Load(configPath); err != nil {
			return err
		}
		if runtime.GOOS != "linux" {
			return errors.New("offline guest maintenance requires a Proxmox Linux host")
		}
		if err := validID(id); err != nil {
			return err
		}
		if _, err := os.Lstat(m.statePath(id)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("run pvedisk unmount or pvedisk clean for your existing repair session first")
		}
		token := make([]byte, 32)
		if _, err := rand.Read(token); err != nil {
			return err
		}
		s := &State{VMID: id, Token: hex.EncodeToString(token), Phase: phase + "-pending"}
		var err error
		s.Worker, err = identity(os.Getpid())
		if err != nil {
			return err
		}
		if err := m.save(s); err != nil {
			return err
		}
		return m.maintain(ctx, s, phase, proxmox.New(), assertDisksUnused, operation)
	})
}

// maintain runs only while the operation lock is held. A failed transition
// leaves the persistent record and startup protection for explicit cleanup.
func (m *Manager) maintain(ctx context.Context, s *State, phase string, p maintenanceProtection, unused func([]proxmox.Disk) error, operation func(*State) error) error {
	disks, err := p.Inspect(ctx, s.VMID)
	if err != nil {
		return m.fail(s, err)
	}
	s.Disks = disks
	s.DiskIdentities, err = captureDiskIdentities(disks)
	if err != nil {
		return m.fail(s, err)
	}
	if err := m.save(s); err != nil {
		return err
	}
	if err := p.Shutdown(ctx, s.VMID); err != nil {
		return m.fail(s, err)
	}
	s.Phase = "acquiring"
	s.ProtectionAttempted = true
	if err := m.save(s); err != nil {
		return err
	}
	disks, err = p.Acquire(ctx, s.VMID, s.Token)
	if err != nil {
		return m.fail(s, err)
	}
	s.Disks = disks
	s.DiskIdentities, err = captureDiskIdentities(disks)
	if err != nil {
		return m.fail(s, err)
	}
	s.Phase = phase
	if err := m.save(s); err != nil {
		return err
	}
	if err := unused(disks); err != nil {
		return m.fail(s, err)
	}
	if err := ctx.Err(); err != nil {
		return m.fail(s, err)
	}
	if err := operation(s); err != nil {
		return m.fail(s, err)
	}
	if err := unused(disks); err != nil {
		return m.fail(s, err)
	}
	return m.finish(s, func() error { return p.Release(ctx, s.VMID, s.Token) })
}

func (m *Manager) resetRootOffline(ctx context.Context, s *State, password []byte) error {
	s.PasswordFile = filepath.Join(m.root, ".password-"+s.Token)
	if err := m.save(s); err != nil {
		return err
	}
	file, err := os.OpenFile(s.PasswordFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer scrubPassword(s.PasswordFile, m.root)
	if _, err := file.Write(password); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := runCustomization(ctx, s, "--root-password", "file:"+s.PasswordFile); err != nil {
		return fmt.Errorf("password repair failed; inspect and clean up your session: %w", err)
	}
	return scrubPassword(s.PasswordFile, m.root)
}

func runCustomization(ctx context.Context, s *State, options ...string) error {
	var args []string
	for _, disk := range s.Disks {
		args = append(args, "--format", disk.Format, "-a", disk.Path)
	}
	args = append(args, options...)
	cmd := exec.CommandContext(ctx, "virt-customize", args...)
	cmd.Env = append(os.Environ(), "LIBGUESTFS_BACKEND=direct")
	return cmd.Run()
}

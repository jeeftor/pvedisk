// Package session owns persistent repair sessions and their detached workers.
package session

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jeeftor/pvedisk/internal/appliance"
	"github.com/jeeftor/pvedisk/internal/proxmox"
)

const stateRoot = "/var/lib/pvedisk"

// Identity distinguishes a process from a later reuse of its process identifier.
type Identity struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
	Boot  string `json:"boot"`
}

// State records the repair intent before any protection or disk access begins.
type State struct {
	VMID                int                     `json:"vmid"`
	Token               string                  `json:"token"`
	Phase               string                  `json:"phase"`
	Error               string                  `json:"error,omitempty"`
	Root                string                  `json:"root"`
	Public              string                  `json:"public"`
	Extras              string                  `json:"extras"`
	Staging             string                  `json:"staging"`
	ReadOnly            bool                    `json:"read_only"`
	Worker              Identity                `json:"worker"`
	Appliance           Identity                `json:"appliance"`
	Disks               []proxmox.Disk          `json:"disks"`
	DiskIdentities      map[string]DiskIdentity `json:"disk_identities,omitempty"`
	Inventory           appliance.Inventory     `json:"inventory"`
	ViewsCreated        bool                    `json:"views_created"`
	CreatedPaths        []string                `json:"created_paths,omitempty"`
	PasswordFile        string                  `json:"password_file,omitempty"`
	ProtectionAttempted bool                    `json:"protection_attempted"`
	FuseStopped         bool                    `json:"fuse_stopped"`
}

// Manager coordinates configuration changes and repair operations.
type Manager struct{ root string }

// New returns the manager for the system repair-state directory.
func New() *Manager { return &Manager{root: stateRoot} }

func (m *Manager) prepare() error {
	if os.Geteuid() != 0 && m.root == stateRoot {
		return errors.New("you must run pvedisk as root")
	}
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return err
	}
	return m.inspectRoot()
}

func (m *Manager) inspectRoot() error {
	if os.Geteuid() != 0 && m.root == stateRoot {
		return errors.New("you must run pvedisk as root")
	}
	fi, err := os.Lstat(m.root)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm()&0077 != 0 {
		return errors.New("repair-state directory must be a private directory")
	}
	if stat, ok := fi.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("repair-state directory ownership is unsafe")
	}
	return nil
}

func (m *Manager) locked(fn func() error) error {
	return m.lockedContext(context.Background(), fn)
}

func (m *Manager) lockedContext(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.prepare(); err != nil {
		return err
	}
	fd, err := syscall.Open(filepath.Join(m.root, "operations.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "operations.lock")
	defer f.Close()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

// WithSetupLock serializes configuration changes against all disk operations.
func (m *Manager) WithSetupLock(fn func() error) error { return m.locked(fn) }

func (m *Manager) statePath(id int) string  { return filepath.Join(m.root, strconv.Itoa(id)+".json") }
func (m *Manager) socketPath(id int) string { return filepath.Join(m.root, strconv.Itoa(id)+".sock") }

func validID(id int) error {
	if id < 100 || id > 999999999 {
		return errors.New("your VM identifier must be between 100 and 999999999")
	}
	return nil
}

// HasSessions reports active or unresolved sessions, including malformed state.
// It does not acquire the operation lock so callers can use it within setup.
func (m *Manager) HasSessions() (bool, error) {
	entries, err := os.ReadDir(m.root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			return true, nil
		}
	}
	return false, nil
}

// Status reads session state without requiring site configuration.
func (m *Manager) Status(id int) (*State, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := m.prepare(); err != nil {
		return nil, err
	}
	return m.readState(id)
}

func (m *Manager) readState(id int) (*State, error) {
	fi, err := os.Lstat(m.statePath(id))
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe session state file")
	}
	data, err := os.ReadFile(m.statePath(id))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("read session state: %w", err)
	}
	if s.VMID != id || len(s.Token) != 64 {
		return nil, errors.New("invalid session ownership state")
	}
	if _, err := hex.DecodeString(s.Token); err != nil {
		return nil, errors.New("invalid session token")
	}
	if err := m.validatePaths(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (m *Manager) validatePaths(s *State) error {
	allowed := map[string]bool{}
	if s.Root != "" {
		if !filepath.IsAbs(s.Root) || filepath.Clean(s.Root) != s.Root || s.Root == "/" {
			return errors.New("invalid recorded mount root")
		}
		if s.Public != filepath.Join(s.Root, fmt.Sprintf("vm%d", s.VMID)) || s.Extras != filepath.Join(s.Root, fmt.Sprintf("vm%d-extra", s.VMID)) || s.Staging != filepath.Join(m.root, fmt.Sprintf("vm%d-stage", s.VMID)) {
			return errors.New("recorded session paths do not match their VM")
		}
		allowed[s.Public] = true
		allowed[s.Extras] = true
		allowed[s.Staging] = true
	} else if s.Public != "" || s.Extras != "" || s.Staging != "" {
		return errors.New("recorded session has paths without a mount root")
	}
	for _, path := range s.CreatedPaths {
		if !allowed[path] {
			return errors.New("invalid recorded created directory")
		}
		delete(allowed, path)
	}
	if s.PasswordFile != "" && s.PasswordFile != filepath.Join(m.root, ".password-"+s.Token) {
		return errors.New("invalid recorded password file")
	}
	for _, disk := range s.Disks {
		if !filepath.IsAbs(disk.Path) || disk.Format == "" {
			return errors.New("invalid recorded disk")
		}
	}
	for path, identity := range s.DiskIdentities {
		found := false
		for _, disk := range s.Disks {
			if disk.Path == path {
				found = true
				break
			}
		}
		if !found || identity.Inode == 0 || (identity.Kind != "file" && identity.Kind != "block") {
			return errors.New("invalid recorded disk identity")
		}
	}
	return nil
}

func (m *Manager) save(s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.root, ".state-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, m.statePath(s.VMID)); err != nil {
		return err
	}
	d, err := os.Open(m.root)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func identity(pid int) (Identity, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Identity{}, err
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Identity{}, err
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return Identity{}, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 20 {
		return Identity{}, errors.New("short process stat")
	}
	return Identity{PID: pid, Start: fields[19], Boot: strings.TrimSpace(string(boot))}, nil
}

func alive(w Identity) (bool, error) {
	if w.PID == 0 {
		return false, nil
	}
	i, err := identity(w.PID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return i == w, nil
}

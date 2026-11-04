// Package proxmox protects VM disks using the host's Proxmox configuration locks.
package proxmox

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Disk identifies an accessible attached disk and its explicit image format.
type Disk struct {
	Path   string `json:"path"`
	Format string `json:"format"`
}

// VM describes a local VM and any known reason that prevents disk repair.
type VM struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	Status            string `json:"status"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

//go:embed helper.pl
var helper string

type runner func(context.Context, string, []string, string) ([]byte, error)

// Client invokes the installed Proxmox tools without a shell.
type Client struct {
	run  runner
	euid func() int
}

// New creates a client for the local Proxmox host.
func New() *Client { return &Client{run: execute, euid: os.Geteuid} }

func execute(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewBufferString(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, stderr.String())
	}
	return out, nil
}

func (c *Client) call(ctx context.Context, operation string, id int, token string) ([]Disk, error) {
	if c.euid() != 0 {
		return nil, errors.New("you must run disk operations as root")
	}
	if id < 100 || id > 999999999 {
		return nil, errors.New("VM ID must be between 100 and 999999999")
	}
	out, err := c.run(ctx, "perl", []string{"-e", helper, "--", operation, strconv.Itoa(id), token}, "")
	if err != nil {
		return nil, fmt.Errorf("Proxmox %s VM %d: %w", operation, id, err)
	}
	var disks []Disk
	if err := json.Unmarshal(out, &disks); err != nil {
		return nil, fmt.Errorf("decode Proxmox %s result: %w", operation, err)
	}
	return disks, nil
}

// List reads local VM names, states, and repair restrictions without changing VM state.
func (c *Client) List(ctx context.Context) ([]VM, error) {
	if c.euid() != 0 {
		return nil, errors.New("you must list Proxmox VMs as root")
	}
	out, err := c.run(ctx, "perl", []string{"-e", helper, "--", "list"}, "")
	if err != nil {
		return nil, fmt.Errorf("list local Proxmox VMs: %w", err)
	}
	var vms []VM
	if err := json.Unmarshal(out, &vms); err != nil {
		return nil, fmt.Errorf("decode local Proxmox VM list: %w", err)
	}
	if vms == nil {
		return nil, errors.New("decode local Proxmox VM list: expected an array")
	}
	seen := make(map[int]bool, len(vms))
	for _, vm := range vms {
		if vm.ID < 100 || vm.ID > 999999999 || vm.Name == "" || vm.Status == "" || seen[vm.ID] {
			return nil, errors.New("decode local Proxmox VM list: invalid or duplicate VM metadata")
		}
		seen[vm.ID] = true
	}
	return vms, nil
}

// Inspect validates lifecycle constraints and discovers attached disk paths.
func (c *Client) Inspect(ctx context.Context, id int) ([]Disk, error) {
	return c.call(ctx, "inspect", id, "")
}

// Acquire atomically rechecks stopped state and records an owned create lock.
func (c *Client) Acquire(ctx context.Context, id int, token string) ([]Disk, error) {
	if !validToken(token) {
		return nil, errors.New("invalid session ownership token")
	}
	return c.call(ctx, "acquire", id, token)
}

// Release removes only a lock whose token and configuration digest still match.
func (c *Client) Release(ctx context.Context, id int, token string) error {
	if !validToken(token) {
		return errors.New("invalid session ownership token")
	}
	_, err := c.call(ctx, "release", id, token)
	return err
}

func validToken(token string) bool {
	if len(token) < 16 || len(token) > 128 {
		return false
	}
	for _, r := range token {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Shutdown requests graceful shutdown, allowing at most 120 seconds and no force stop.
func (c *Client) Shutdown(ctx context.Context, id int) error {
	if _, err := c.Inspect(ctx, id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 125*time.Second)
	defer cancel()
	_, err := c.run(ctx, "qm", []string{"shutdown", strconv.Itoa(id), "--timeout", "120", "--forceStop", "0"}, "")
	if err != nil {
		return fmt.Errorf("graceful shutdown VM %d (no forced power off): %w", id, err)
	}
	return nil
}

// Doctor checks the local platform and required lifecycle tools/modules.
func (c *Client) Doctor(ctx context.Context) error {
	if c.euid() != 0 {
		return errors.New("you must run doctor as root")
	}
	for _, name := range []string{"qm", "pvesm", "perl", "qemu-img", "virt-customize", "mount", "umount"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("required runtime tool %s: %w", name, err)
		}
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			return fmt.Errorf("required FUSE unmount tool (fusermount3 or fusermount): %w", err)
		}
	}
	fuse, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open FUSE device /dev/fuse: %w", err)
	}
	if err := fuse.Close(); err != nil {
		return fmt.Errorf("close FUSE device: %w", err)
	}
	_, err = c.run(ctx, "perl", []string{"-e", helper, "--", "doctor", "100", ""}, "")
	return err
}

package proxmox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverAndAcquire(t *testing.T) {
	for _, operation := range []string{"inspect", "acquire"} {
		t.Run(operation, func(t *testing.T) {
			c := &Client{euid: func() int { return 0 }, run: func(_ context.Context, name string, args []string, _ string) ([]byte, error) {
				if name != "perl" || len(args) != 6 || args[3] != operation || args[4] != "400" {
					t.Fatalf("unexpected invocation: %s %v", name, args)
				}
				return []byte(`[{"path":"/dev/zvol/pool/vm-400-disk-0","format":"raw"}]`), nil
			}}
			var disks []Disk
			var err error
			if operation == "inspect" {
				disks, err = c.Inspect(context.Background(), 400)
			} else {
				disks, err = c.Acquire(context.Background(), 400, "0123456789abcdef")
			}
			if err != nil || !reflect.DeepEqual(disks, []Disk{{Path: "/dev/zvol/pool/vm-400-disk-0", Format: "raw"}}) {
				t.Fatalf("disks=%v err=%v", disks, err)
			}
		})
	}
}

func TestRejectsBeforeInvokingTools(t *testing.T) {
	c := &Client{euid: func() int { return 0 }, run: func(context.Context, string, []string, string) ([]byte, error) {
		t.Fatal("unexpected tool execution")
		return nil, nil
	}}
	if _, err := c.Inspect(context.Background(), 99); err == nil {
		t.Fatal("accepted invalid VM ID")
	}
	if _, err := c.Acquire(context.Background(), 400, "bad\ntoken"); err == nil {
		t.Fatal("accepted invalid ownership token")
	}
	if err := c.Release(context.Background(), 400, ""); err == nil {
		t.Fatal("accepted missing ownership token")
	}
	c.euid = func() int { return 1000 }
	if _, err := c.Inspect(context.Background(), 400); err == nil {
		t.Fatal("accepted nonroot caller")
	}
}

func TestShutdownNeverForcesAndInspectsFirst(t *testing.T) {
	var names []string
	c := &Client{euid: func() int { return 0 }, run: func(ctx context.Context, name string, args []string, _ string) ([]byte, error) {
		names = append(names, name)
		if name == "perl" {
			return []byte(`[{"path":"/disk","format":"raw"}]`), nil
		}
		if !reflect.DeepEqual(args, []string{"shutdown", "400", "--timeout", "120", "--forceStop", "0"}) {
			t.Fatalf("unsafe shutdown args: %v", args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("shutdown has no deadline")
		}
		return nil, errors.New("timeout")
	}}
	if err := c.Shutdown(context.Background(), 400); err == nil || !strings.Contains(err.Error(), "no forced power off") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"perl", "qm"}) {
		t.Fatalf("unexpected commands: %v", names)
	}
}

func TestRejectedLifecycleDoesNotShutdown(t *testing.T) {
	c := &Client{euid: func() int { return 0 }, run: func(_ context.Context, name string, _ []string, _ string) ([]byte, error) {
		if name != "perl" {
			t.Fatalf("shutdown ran despite rejected inspection: %s", name)
		}
		return nil, errors.New("VM is HA-managed")
	}}
	if err := c.Shutdown(context.Background(), 400); err == nil {
		t.Fatal("ignored inspection failure")
	}
}

func TestReleasePreservesUncertainProtection(t *testing.T) {
	c := &Client{euid: func() int { return 0 }, run: func(_ context.Context, _ string, args []string, _ string) ([]byte, error) {
		if args[3] != "release" || args[5] != "0123456789abcdef" {
			t.Fatalf("unexpected release: %v", args)
		}
		return nil, errors.New("ownership mismatch; lock retained")
	}}
	if err := c.Release(context.Background(), 400, "0123456789abcdef"); err == nil {
		t.Fatal("lost release failure")
	}
}

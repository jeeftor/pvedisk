//go:build linux

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/proxmox"
)

func TestMountRefusesRunningVMBeforeStaleCleanup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Proxmox operations require root")
	}
	m := testManager(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := m.intent(400, root, false)
	if err != nil {
		t.Fatal(err)
	}
	s.Worker.PID = 1 << 30
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(m.statePath(400))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("mount_root: "+root+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	diagnosticTool(t, bin, "perl", `[ "$4" = stopped ] || exit 2
printf '%s\n' 'VM 400 is running; stop it first with: qm shutdown 400' >&2
exit 1
`)
	t.Setenv("PATH", bin)
	for _, readOnly := range []bool{false, true} {
		err := m.Mount(context.Background(), 400, configPath, readOnly, func(string) { t.Fatal("refused mount started recovery or worker") })
		if err == nil || !strings.Contains(err.Error(), "qm shutdown 400") {
			t.Fatalf("running mount diagnostic=%v", err)
		}
		after, err := os.ReadFile(m.statePath(400))
		if err != nil || string(after) != string(before) {
			t.Fatal("running VM refusal changed stale state")
		}
		if _, err := os.Stat(filepath.Join(m.root, "400.log")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("running VM refusal started a worker")
		}
	}
}

func TestMountRecoversStaleSessionAndAllowsNewIntent(t *testing.T) {
	m, s := cleanupSession(t)
	s.Worker = Identity{PID: 1 << 30, Start: "old", Boot: "old"}
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	var progress []string
	if err := m.cleanStaleForMount(context.Background(), 400, func(phase string) { progress = append(progress, phase) }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(progress, []string{"cleaning-stale", "cleaned-stale"}) {
		t.Fatalf("recovery progress=%v", progress)
	}
	next, err := m.intent(400, s.Root, true)
	if err != nil {
		t.Fatalf("stale session still blocks mounting: %v", err)
	}
	if !next.ReadOnly || next.Token == s.Token {
		t.Fatal("new mount did not preserve options or create fresh ownership")
	}
	if err := m.removeState(next); err != nil {
		t.Fatal(err)
	}
}

func TestMountRecoveryRetainsUnresolvedSessions(t *testing.T) {
	for _, scenario := range []string{"pending-worker", "live-worker", "live-appliance", "unexpected-data", "missing-disk"} {
		t.Run(scenario, func(t *testing.T) {
			m, s := cleanupSession(t)
			s.Worker.PID = 1 << 30
			want := "could not be cleaned safely"
			self, err := identity(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "pending-worker":
				s.Worker = Identity{}
				want = ErrSessionExists.Error()
			case "live-worker":
				s.Worker = self
				want = ErrSessionExists.Error()
			case "live-appliance":
				s.Appliance = self
			case "unexpected-data":
				if err := os.WriteFile(s.Public+"/keep", []byte("host data"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-disk":
				s.Disks = []proxmox.Disk{{Path: s.Root + "/missing.img", Format: "raw"}}
			}
			if err := m.save(s); err != nil {
				t.Fatal(err)
			}
			err = m.cleanStaleForMount(context.Background(), 400, nil)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("unsafe recovery=%v", err)
			}
			if _, err := m.readState(400); err != nil {
				t.Fatal("unsafe recovery discarded session")
			}
			if scenario == "unexpected-data" {
				if err := os.Remove(s.Public + "/keep"); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.finish(s, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMountRecoveryWithNoSession(t *testing.T) {
	m := testManager(t)
	if err := m.cleanStaleForMount(context.Background(), 400, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.readState(400); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery created a session")
	}
}

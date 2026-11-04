package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/proxmox"
	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/spf13/cobra"
)

func pickerOperations() operations {
	ops := testOperations()
	ops.load = func(string) (config.Config, error) { return config.Config{MountRoot: "/repairs"}, nil }
	ops.terminal = func(*cobra.Command) bool { return true }
	return ops
}

func mountedDescription(id int) session.Description {
	return session.Description{VMID: id, State: &session.State{VMID: id, Phase: "mounted", Public: "/repairs/vm400"}, Observation: session.Observation{Status: "mounted", WorkerAlive: true, WorkerKnown: true, MountsKnown: true, RootMounted: true, ExtrasMounted: true, StagingMounted: true}}
}

func TestMountPickerShowsLocalStateAndRequiresAvailableListedID(t *testing.T) {
	for _, test := range []struct {
		input     string
		wantMount bool
	}{{"400\n", true}, {"401\n", false}, {"402\n", false}, {"999\n", false}, {"\n", false}, {"", false}} {
		t.Run(strings.TrimSpace(test.input), func(t *testing.T) {
			ops := pickerOperations()
			ops.listVMs = func(context.Context) ([]proxmox.VM, error) {
				return []proxmox.VM{{ID: 400, Name: "my-linux", Status: "running"}, {ID: 401, Name: "locked-vm", Status: "stopped", UnavailableReason: "VM is HA-managed"}, {ID: 402, Name: "existing-repair", Status: "stopped"}}, nil
			}
			ops.listSessions = func() ([]session.Description, error) { return []session.Description{mountedDescription(402)}, nil }
			called := false
			ops.mount = func(_ context.Context, id int, _ string, _ bool, _ func(string)) error {
				called = id == 400
				return nil
			}
			ops.status = func(int) (*session.State, error) { return &session.State{Public: "/repairs/vm400"}, nil }
			output, err := executeInput(ops, test.input, "mount")
			if called != test.wantMount || (err == nil) != test.wantMount {
				t.Fatalf("called=%v output=%q err=%v", called, output, err)
			}
			for _, want := range []string{"Your local VMs", "my-linux", "running", "HA-managed", "existing repair session", "VM ID:"} {
				if !strings.Contains(output, want) {
					t.Fatalf("missing %q in %q", want, output)
				}
			}
		})
	}
}

func TestMountPickerListingErrorsAndNoTargetsNeverMount(t *testing.T) {
	for _, test := range []struct {
		name              string
		vms               []proxmox.VM
		vmErr, sessionErr error
	}{
		{"permission", nil, os.ErrPermission, nil},
		{"no-local-vms", nil, nil, nil},
		{"session-error", []proxmox.VM{{ID: 400}}, nil, os.ErrPermission},
		{"all-unavailable", []proxmox.VM{{ID: 400, UnavailableReason: "locked"}}, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := pickerOperations()
			ops.listVMs = func(context.Context) ([]proxmox.VM, error) { return test.vms, test.vmErr }
			ops.listSessions = func() ([]session.Description, error) { return nil, test.sessionErr }
			ops.mount = func(context.Context, int, string, bool, func(string)) error {
				t.Fatal("mounted without available selection")
				return nil
			}
			output, err := executeInput(ops, "400\n", "mount")
			if err == nil || strings.Contains(output, "VM ID:") {
				t.Fatalf("output=%q err=%v", output, err)
			}
			if test.vmErr != nil && !errors.Is(err, test.vmErr) {
				t.Fatal("lost listing permission error")
			}
		})
	}
}

func TestNoninteractivePickersNeverListOrPrompt(t *testing.T) {
	for _, action := range []string{"mount", "unmount"} {
		ops := testOperations()
		ops.load = func(string) (config.Config, error) { return config.Config{MountRoot: "/repairs"}, nil }
		ops.listVMs = func(context.Context) ([]proxmox.VM, error) { t.Fatal("listed without terminal"); return nil, nil }
		ops.listSessions = func() ([]session.Description, error) { t.Fatal("listed without terminal"); return nil, nil }
		output, err := executeInput(ops, "400\n", action)
		if err == nil || strings.Contains(output, "VM ID:") {
			t.Fatalf("action=%s output=%q err=%v", action, output, err)
		}
	}
}

func TestUnmountPickerIncludesRecoveryRecordsButSelectsOnlyLiveOwnedMounts(t *testing.T) {
	for _, id := range []string{"400", "401", "402", "403", "999", ""} {
		t.Run(id, func(t *testing.T) {
			ops := pickerOperations()
			ops.load = func(string) (config.Config, error) {
				t.Fatal("unmount picker read configuration")
				return config.Config{}, nil
			}
			incomplete := mountedDescription(401)
			incomplete.Observation.WorkerAlive = false
			incomplete.Observation.Status = "partial"
			conflict := mountedDescription(402)
			conflict.Observation.Status = "conflict"
			ops.listSessions = func() ([]session.Description, error) {
				return []session.Description{mountedDescription(400), incomplete, conflict, {VMID: 403, Error: "invalid ownership state"}}, nil
			}
			called := false
			ops.unmount = func(_ context.Context, id int) error { called = id == 400; return nil }
			output, err := executeInput(ops, id+"\n", "unmount")
			want := id == "400"
			if called != want || (err == nil) != want {
				t.Fatalf("called=%v output=%q err=%v", called, output, err)
			}
			for _, text := range []string{"pvedisk unmount 400", "pvedisk recover 401", "pvedisk recover 402", "invalid ownership state"} {
				if !strings.Contains(output, text) {
					t.Fatalf("missing %q in %q", text, output)
				}
			}
		})
	}
}

func TestEmptyUnmountPickerDoesNotPrompt(t *testing.T) {
	output, err := executeInput(pickerOperations(), "400\n", "unmount")
	if err == nil || strings.Contains(output, "VM ID:") || !strings.Contains(output, "no recorded pvedisk repair sessions") {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestUnmountPickerCanRetryBusyDiskCleanupAfterViewsDisappear(t *testing.T) {
	for _, phase := range []string{"failed", "finalizing", "finalized"} {
		t.Run(phase, func(t *testing.T) {
			ops := pickerOperations()
			description := mountedDescription(400)
			description.State.Phase = phase
			description.State.Error = "disk is still open by process 1234"
			description.Observation.Status = "not-mounted"
			description.Observation.RootMounted = false
			description.Observation.ExtrasMounted = false
			description.Observation.StagingMounted = false
			ops.listSessions = func() ([]session.Description, error) { return []session.Description{description}, nil }
			called := false
			ops.unmount = func(context.Context, int) error { called = true; return nil }
			output, err := executeInput(ops, "400\n", "unmount")
			if err != nil || !called || !strings.Contains(output, "disk is still open by process 1234") || !strings.Contains(output, "pvedisk unmount 400") {
				t.Fatalf("called=%v output=%q err=%v", called, output, err)
			}
		})
	}
}

func TestStatusListsActualMountsInsteadOfAssumingRecordedPhase(t *testing.T) {
	ops := testOperations()
	ops.load = func(string) (config.Config, error) { t.Fatal("status read configuration"); return config.Config{}, nil }
	description := mountedDescription(400)
	description.Observation = session.Observation{Status: "not-mounted", WorkerKnown: true, MountsKnown: true}
	ops.listSessions = func() ([]session.Description, error) { return []session.Description{description}, nil }
	output, err := execute(ops, "status")
	for _, want := range []string{"ACTUAL MOUNTS", "RECORDED PHASE", "not-mounted", "mounted", "pvedisk recover 400"} {
		if err != nil || !strings.Contains(output, want) {
			t.Fatalf("missing %q output=%q err=%v", want, output, err)
		}
	}
	output, err = execute(ops, "status", "--json")
	var descriptions []session.Description
	if err != nil || json.Unmarshal([]byte(output), &descriptions) != nil || len(descriptions) != 1 || descriptions[0].Observation.Status != "not-mounted" {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestEmptyStatusIsReadableAndEmptyJSONIsArray(t *testing.T) {
	ops := testOperations()
	output, err := execute(ops, "status")
	if err != nil || !strings.Contains(output, "no recorded pvedisk repair sessions") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	output, err = execute(ops, "status", "--json")
	if err != nil || strings.TrimSpace(output) != "[]" {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

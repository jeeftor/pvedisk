package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/proxmox"
	"github.com/jeeftor/pvedisk/internal/session"
)

func executePS(ops operations, args ...string) (string, string, error) {
	command := newCommand(ops)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetIn(strings.NewReader(""))
	command.SetArgs(append([]string{"ps"}, args...))
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func workerDescription(id, pid int, status string, known, alive, readOnly bool) session.Description {
	return session.Description{
		VMID:        id,
		State:       &session.State{VMID: id, Token: "private-ownership-token", Phase: "mounted", Public: "/repair/vm" + strconv.Itoa(id), ReadOnly: readOnly, Worker: session.Identity{PID: pid}},
		Observation: session.Observation{Status: status, WorkerKnown: known, WorkerAlive: alive},
	}
}

func TestPSShowsConfirmedLiveWorkersAndTheirRealMountStatus(t *testing.T) {
	ops := testOperations()
	running := workerDescription(408, 2001, "mounted", true, true, false)
	readOnly := workerDescription(409, 2002, "mounted", true, true, true)
	starting := workerDescription(410, 2003, "not-mounted", true, true, true)
	starting.State.Phase = "launching"
	failed := workerDescription(411, 2004, "not-mounted", true, true, false)
	failed.State.Phase = "failed"
	failed.State.Error = "a private internal error"
	ops.listSessions = func() ([]session.Description, error) {
		return []session.Description{running, readOnly, starting, failed}, nil
	}
	stdout, stderr, err := executePS(ops)
	if err != nil || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	for _, want := range []string{"VMID", "PID", "MODE", "MOUNTS", "PATH", "408", "409", "2001", "2002", "read/write", "read-only", "not mounted", "/repair/vm410", "/repair/vm411"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q in %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "private-ownership-token") || strings.Contains(stdout, "private internal error") {
		t.Fatal("ps disclosed internal session details")
	}
	for _, line := range strings.Split(stdout, "\n") {
		if (strings.HasPrefix(line, "410 ") || strings.HasPrefix(line, "411 ")) && !strings.Contains(line, "not mounted") {
			t.Fatalf("live startup/failed worker falsely marked mounted: %q", line)
		}
	}
}

func TestPSFiltersDeadStaleUnknownAndCorruptWorkersWithGuidance(t *testing.T) {
	ops := testOperations()
	deadWithMounts := workerDescription(401, 3001, "partial", true, false, false)
	stale := workerDescription(402, 3002, "not-mounted", true, false, false)
	unknown := workerDescription(403, 3003, "unknown", false, false, false)
	corrupt := session.Description{VMID: 404, Error: "invalid ownership state"}
	ops.listSessions = func() ([]session.Description, error) {
		return []session.Description{deadWithMounts, stale, unknown, corrupt}, nil
	}
	stdout, stderr, err := executePS(ops)
	if err != nil || stderr != "" || !strings.HasPrefix(stdout, "No running pvedisk workers.\n") || !strings.Contains(stdout, "Saved sessions without a running worker: 2.") || !strings.Contains(stdout, "Saved sessions whose worker could not be checked: 2.") || !strings.Contains(stdout, "pvedisk status") || !strings.Contains(stdout, "their disks may still be mounted and need cleanup") {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	for _, pid := range []string{"3001", "3002", "3003"} {
		if strings.Contains(stdout, pid) {
			t.Fatalf("unconfirmed worker displayed as running: %q", stdout)
		}
	}
}

func TestPSJSONContainsOnlyPublicFieldsAndKeepsGuidanceSeparate(t *testing.T) {
	ops := testOperations()
	ops.listSessions = func() ([]session.Description, error) {
		return []session.Description{workerDescription(408, 2001, "mounted", true, true, false), workerDescription(409, 2002, "partial", true, false, true), {VMID: 410, Error: "unreadable"}}, nil
	}
	stdout, stderr, err := executePS(ops, "--json")
	if err != nil || !json.Valid([]byte(stdout)) || strings.Contains(stdout, "\x1b") || strings.Contains(stdout, "private-ownership-token") || !strings.Contains(stderr, "Saved sessions without a running worker: 1.") || !strings.Contains(stderr, "Saved sessions whose worker could not be checked: 1.") || !strings.Contains(stderr, "pvedisk status") {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || len(entries[0]) != 5 {
		t.Fatalf("unexpected public worker details: %+v", entries)
	}
	entry := entries[0]
	if entry["vmid"] != float64(408) || entry["pid"] != float64(2001) || entry["mode"] != "read/write" || entry["mount_status"] != "mounted" || entry["path"] != "/repair/vm408" {
		t.Fatalf("incorrect public worker details: %+v", entry)
	}
}

func TestPSHasExactEmptyResults(t *testing.T) {
	ops := testOperations()
	stdout, stderr, err := executePS(ops)
	if err != nil || stdout != "No running pvedisk workers.\n" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, stderr, err = executePS(ops, "--json")
	if err != nil || stdout != "[]\n" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

func TestPSDoesNotLoadConfigurationOrOperateOnVMs(t *testing.T) {
	ops := testOperations()
	ops.load = func(string) (config.Config, error) { t.Fatal("ps loaded configuration"); return config.Config{}, nil }
	ops.listVMs = func(context.Context) ([]proxmox.VM, error) { t.Fatal("ps queried Proxmox inventory"); return nil, nil }
	ops.mount = func(context.Context, int, string, bool, func(string)) error { t.Fatal("ps mounted a VM"); return nil }
	ops.unmount = func(context.Context, int) error { t.Fatal("ps unmounted a VM"); return nil }
	ops.recover = func(context.Context, int) error { t.Fatal("ps recovered a VM"); return nil }
	if _, _, err := executePS(ops); err != nil {
		t.Fatal(err)
	}
}

func TestPSPreservesListingErrorsWithoutClaimingEmpty(t *testing.T) {
	ops := testOperations()
	ops.listSessions = func() ([]session.Description, error) { return nil, os.ErrPermission }
	stdout, _, err := executePS(ops)
	if !errors.Is(err, os.ErrPermission) || stdout != "" {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
}

func TestPSRejectsImpossibleLiveWorkerRecords(t *testing.T) {
	ops := testOperations()
	missingPID := workerDescription(400, 0, "mounted", true, true, false)
	mismatched := workerDescription(401, 2000, "mounted", true, true, false)
	mismatched.State.VMID = 999
	ops.listSessions = func() ([]session.Description, error) { return []session.Description{missingPID, mismatched}, nil }
	stdout, _, err := executePS(ops)
	if err != nil || !strings.Contains(stdout, "No running pvedisk workers.") || !strings.Contains(stdout, "Saved sessions whose worker could not be checked: 2.") {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
}

func TestPSExplainsSavedSessionsWithoutSuggestingTheirDisksAreUnmounted(t *testing.T) {
	ops := testOperations()
	ops.listSessions = func() ([]session.Description, error) {
		return []session.Description{workerDescription(407, 2808155, "mounted", true, true, false), workerDescription(408, 3001, "partial", true, false, false)}, nil
	}
	stdout, stderr, err := executePS(ops)
	if err != nil || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	for _, want := range []string{"407", "2808155", "Saved sessions without a running worker: 1.", "their disks may still be mounted and need cleanup", "pvedisk status"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q in %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "could not be checked") || strings.Contains(stdout, "inactive") || strings.Contains(stdout, "\x1b") {
		t.Fatalf("misleading or colored redirected summary: %q", stdout)
	}
}

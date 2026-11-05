package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/config"
	"github.com/jeeftor/pvedisk/internal/session"
)

func TestIsMountedUsesCurrentObservationsWithoutChangingYourSession(t *testing.T) {
	mounted := mountedDescription(400)
	readOnly := mounted
	readOnly.State = &session.State{VMID: 400, ReadOnly: true}
	unmounted := mounted
	unmounted.Observation = session.Observation{Status: "not-mounted", MountsKnown: true, WorkerKnown: true}
	starting := unmounted
	starting.Observation.WorkerAlive = true
	for _, tc := range []struct {
		name        string
		description *session.Description
		err         error
		code        int
	}{
		{"mounted writable", &mounted, nil, 0},
		{"mounted read only", &readOnly, nil, 0},
		{"no session", nil, os.ErrNotExist, 1},
		{"wrapped missing session", nil, fmt.Errorf("read your session: %w", os.ErrNotExist), 1},
		{"saved mounted step but no actual mounts", &unmounted, nil, 1},
		{"worker without mounts", &starting, nil, 1},
		{"permission denied", nil, os.ErrPermission, 2},
		{"damaged state", nil, errors.New("invalid session ownership state"), 2},
		{"no description", nil, nil, 2},
		{"missing state", &session.Description{}, nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := testOperations()
			ops.load = func(string) (config.Config, error) {
				t.Fatal("probe loaded your site configuration")
				return config.Config{}, nil
			}
			ops.clean = func(context.Context, int) error { t.Fatal("probe cleaned a session"); return nil }
			ops.unmount = func(context.Context, int) error { t.Fatal("probe unmounted a session"); return nil }
			calls := 0
			ops.describe = func(id int) (*session.Description, error) {
				calls++
				if id != 400 {
					t.Fatalf("inspected VM %d", id)
				}
				return tc.description, tc.err
			}
			output, err := execute(ops, "--config", "/missing.yaml", "ismounted", "400")
			assertMountProbeResult(t, output, err, tc.code)
			if calls != 1 {
				t.Fatalf("inspection calls=%d", calls)
			}
			if tc.err != nil && tc.code == 2 && !errors.Is(err, tc.err) {
				t.Fatalf("lost inspection error: %v", err)
			}
		})
	}
}

func TestIsMountedReportsUncertainAndInconsistentObservations(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*session.Description)
	}{
		{"partial", func(d *session.Description) { d.Observation.Status = "partial"; d.Observation.ExtrasMounted = false }},
		{"conflicting mount", func(d *session.Description) {
			d.Observation.Status = "conflict"
			d.Observation.Error = "mount belongs to another session"
		}},
		{"unknown", func(d *session.Description) { d.Observation.Status = "unknown" }},
		{"unknown mounts", func(d *session.Description) { d.Observation.MountsKnown = false }},
		{"unknown worker", func(d *session.Description) { d.Observation.WorkerKnown = false }},
		{"dead worker", func(d *session.Description) { d.Observation.WorkerAlive = false }},
		{"missing root", func(d *session.Description) { d.Observation.RootMounted = false }},
		{"missing staging", func(d *session.Description) { d.Observation.StagingMounted = false }},
		{"observation error", func(d *session.Description) { d.Observation.Error = "cannot read mount information" }},
		{"record error", func(d *session.Description) { d.Error = "damaged record" }},
		{"unmounted but has a mount", func(d *session.Description) { d.Observation.Status = "not-mounted" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			description := mountedDescription(400)
			change.apply(&description)
			ops := testOperations()
			ops.describe = func(int) (*session.Description, error) { return &description, nil }
			output, err := execute(ops, "ismounted", "400")
			assertMountProbeResult(t, output, err, 2)
			if !strings.Contains(err.Error(), "pvedisk status 400") {
				t.Fatalf("missing next step: %v", err)
			}
		})
	}
}

func TestIsMountedInvalidArgumentsReturnTwoWithoutInspectingYourHost(t *testing.T) {
	for _, args := range [][]string{
		{"ismounted"}, {"ismounted", "400", "401"}, {"ismounted", "abc"},
		{"ismounted", "99"}, {"ismounted", "1000000000"}, {"ismounted", "400", "--unknown"},
		{"ismounted", "400", "--config"},
	} {
		ops := testOperations()
		ops.describe = func(int) (*session.Description, error) { t.Fatal("invalid probe inspected your host"); return nil, nil }
		output, err := execute(ops, args...)
		assertMountProbeResult(t, output, err, 2)
	}
}

func assertMountProbeResult(t *testing.T, output string, err error, code int) {
	t.Helper()
	if ExitCode(err) != code || output != "" {
		t.Fatalf("stdout=%q error=%v exit=%d, want silent stdout and exit %d", output, err, ExitCode(err), code)
	}
	var stderr bytes.Buffer
	if err != nil && !PrintError(&stderr, err) {
		fmt.Fprintln(&stderr, err)
	}
	if (stderr.Len() != 0) != (code == 2) {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func TestExitCodePreservesOtherCommandFailures(t *testing.T) {
	if ExitCode(nil) != 0 || ExitCode(errors.New("failure")) != 1 {
		t.Fatal("changed ordinary command exit codes")
	}
	var stderr bytes.Buffer
	wrapped := fmt.Errorf("probe: %w", errNotMounted)
	if ExitCode(wrapped) != 1 || !PrintError(&stderr, wrapped) || stderr.Len() != 0 {
		t.Fatal("wrapped unmounted result was not silent")
	}
}

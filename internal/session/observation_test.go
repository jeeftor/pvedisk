package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func observationState() *State {
	return &State{VMID: 400, Token: strings.Repeat("a", 64), Phase: "mounted", Root: "/repair", Public: "/repair/vm400", Extras: "/repair/vm400-extra", Staging: "/private/pvedisk/vm400-stage"}
}

func TestObservationChecksActualMountsSeparatelyFromPhase(t *testing.T) {
	s := observationState()
	owned := "pvedisk-" + s.Token
	root := mountInfoLine(s.Public, "fuse.guestfs", owned)
	extras := mountInfoLine(s.Extras, "fuse.guestfs", owned)
	staging := mountInfoLine(s.Staging, "fuse.guestfs", owned)
	for _, tc := range []struct {
		name, data          string
		live                bool
		status              string
		root, extras, stage bool
	}{
		{"healthy", root + extras + staging, true, "mounted", true, true, true},
		{"stale recorded phase", "", false, "not-mounted", false, false, false},
		{"missing extras", root + staging, true, "partial", true, false, true},
		{"only staging", staging, true, "partial", false, false, true},
		{"missing worker", root + extras + staging, false, "partial", true, true, true},
		{"different token", mountInfoLine(s.Public, "fuse.guestfs", "pvedisk-other"), true, "conflict", true, false, false},
		{"different filesystem", mountInfoLine(s.Public, "ext4", owned), true, "conflict", true, false, false},
		{"unexpected nested mount", root + mountInfoLine(s.Public+"/home", "ext4", "/dev/sda1"), true, "conflict", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := observe(s, tc.data, nil, tc.live, nil)
			if o.Status != tc.status || o.RootMounted != tc.root || o.ExtrasMounted != tc.extras || o.StagingMounted != tc.stage {
				t.Fatalf("observation=%+v", o)
			}
			if !o.MountsKnown || !o.WorkerKnown {
				t.Fatal("known observation reported unknown")
			}
			if s.Phase != "mounted" {
				t.Fatal("observation changed lifecycle state")
			}
			if tc.status == "partial" || tc.status == "conflict" {
				if o.Error == "" {
					t.Fatal("missing explanation")
				}
			}
		})
	}
}

func TestObservationUnknownCannotClaimUnmounted(t *testing.T) {
	s := observationState()
	for _, tc := range []struct {
		name, data          string
		mountErr, workerErr error
	}{
		{"mount read error", "", errors.New("permission denied"), nil},
		{"malformed mount data", "invalid", nil, nil},
		{"worker lookup error", "", nil, errors.New("process inspection denied")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := observe(s, tc.data, tc.mountErr, false, tc.workerErr)
			if o.Status != "unknown" || o.Error == "" {
				t.Fatalf("uncertain observation=%+v", o)
			}
		})
	}
}

func TestObservationUsesRecordedPaths(t *testing.T) {
	s := observationState()
	s.Root = "/old repair"
	s.Public = s.Root + "/vm400"
	s.Extras = s.Root + "/vm400-extra"
	data := mountInfoLine("/old\\040repair/vm400", "fuse.guestfs", "pvedisk-"+s.Token) + mountInfoLine("/new-config/vm400", "ext4", "/dev/sda1")
	o := observe(s, data, nil, true, nil)
	if o.Status != "partial" || !o.RootMounted {
		t.Fatalf("recorded path observation=%+v", o)
	}
}

func TestListEmptyDoesNotCreateStateDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	m := &Manager{root: path}
	rows, err := m.List()
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%v error=%v", rows, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only listing created state directory")
	}
}

func TestListReportsCorruptRecordsAndSortsVMs(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{900, 400} {
		if _, err := m.intent(id, root, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(m.statePath(500), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.root, "unknown.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.root, "00400.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[0].VMID != 400 || rows[1].VMID != 400 || rows[2].VMID != 500 || rows[3].VMID != 900 || rows[4].Record != "unknown.json" {
		t.Fatalf("unexpected row order=%+v", rows)
	}
	for _, row := range rows {
		if row.Record == "500.json" || row.Record == "unknown.json" || row.Record == "00400.json" {
			if row.Error == "" || row.State != nil || row.Observation.Status != "unknown" {
				t.Fatalf("corrupt record omitted or trusted: %+v", row)
			}
		} else if row.State == nil || row.Error != "" {
			t.Fatalf("valid record unreadable: %+v", row)
		}
	}
}

func TestDescriptionIsReadOnlyAndConfigurationIndependent(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := m.intent(400, root, false)
	if err != nil {
		t.Fatal(err)
	}
	s.Phase = "mounted"
	s.CreatedPaths = []string{s.Public, s.Extras, s.Staging}
	for _, path := range s.CreatedPaths {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(m.statePath(400))
	if err != nil {
		t.Fatal(err)
	}
	description, err := m.Describe(400)
	if err != nil {
		t.Fatal(err)
	}
	if description.State.Phase != "mounted" || description.State.Root != s.Root {
		t.Fatalf("record changed=%+v", description)
	}
	if description.Observation.Status == "mounted" {
		t.Fatal("recorded phase falsely presented as actual mounts")
	}
	after, err := os.ReadFile(m.statePath(400))
	if err != nil || string(before) != string(after) {
		t.Fatal("description changed persistent state")
	}
	for _, path := range s.CreatedPaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("description removed cleanup directory")
		}
	}
	rows, err := m.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("read-only list=%+v error=%v", rows, err)
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("read-only queries created files: %+v", entries)
	}
}

func TestListRejectsUnsafeDirectoryWithoutMutation(t *testing.T) {
	m := testManager(t)
	if err := os.Chmod(m.root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.List(); err == nil {
		t.Fatal("unsafe state directory trusted")
	}
	fi, err := os.Stat(m.root)
	if err != nil || fi.Mode().Perm() != 0755 {
		t.Fatal("read-only list changed permissions")
	}
}

package appliance

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeBackend struct {
	roots         []string
	kind          string
	points, files map[string]string
	calls         []string
	fail          string
}

func (f *fakeBackend) record(call string) error {
	f.calls = append(f.calls, call)
	if strings.HasPrefix(call, f.fail) && f.fail != "" {
		return errors.New("injected failure")
	}
	return nil
}
func (f *fakeBackend) Inspect_os() ([]string, error)           { return f.roots, nil }
func (f *fakeBackend) Inspect_get_type(string) (string, error) { return f.kind, nil }
func (f *fakeBackend) Inspect_get_mountpoints(string) (map[string]string, error) {
	return f.points, nil
}
func (f *fakeBackend) List_filesystems() (map[string]string, error) { return f.files, nil }
func (f *fakeBackend) Mkmountpoint(p string) error                  { return f.record("mkdir " + p) }
func (f *fakeBackend) Mount_options(o, d, p string) error {
	return f.record("mount " + o + " " + d + " " + p)
}
func (f *fakeBackend) Umount_all() error      { return f.record("unmount") }
func (f *fakeBackend) Sync() error            { return f.record("sync") }
func (f *fakeBackend) Shutdown() error        { return f.record("shutdown") }
func (f *fakeBackend) Get_pid() (int, error)  { return 123, nil }
func (f *fakeBackend) Mount_local_run() error { return f.record("run") }
func fixture() (*Handle, *fakeBackend) {
	f := &fakeBackend{roots: []string{"/dev/sda1"}, kind: "linux", points: map[string]string{"/": "/dev/sda1", "/home": "/dev/vg/home", "/boot": "/dev/sda2"}, files: map[string]string{"/dev/sda1": "ext4", "/dev/sda2": "ext4", "/dev/vg/home": "xfs", "/dev/sdb1": "ext4", "/dev/sdb2": "swap", "/dev/sdc1": "LVM2_member"}}
	h := &Handle{g: f, export: func(string, bool, string) error { return nil }, close: func() error { return f.record("close") }}
	return h, f
}

func TestExportRequiresSafeOwnership(t *testing.T) {
	for _, token := range []string{"", "token,allow_other", "token with spaces", "token\n", strings.Repeat("x", 129)} {
		h, _ := fixture()
		h.prepared = true
		called := false
		h.export = func(string, bool, string) error { called = true; return nil }
		if err := h.Export("/stage", token); err == nil || called {
			t.Fatalf("unsafe token %q reached export", token)
		}
	}
	h, _ := fixture()
	h.prepared = true
	got := ""
	h.export = func(_ string, _ bool, token string) error { got = token; return nil }
	if err := h.Export("/stage", "a1-b2_C3"); err != nil {
		t.Fatal(err)
	}
	if got != "a1-b2_C3" {
		t.Fatalf("ownership = %q", got)
	}
}

func TestPrepareHierarchyAndExtras(t *testing.T) {
	h, f := fixture()
	h.readonly = true
	inventory, err := h.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Filesystems) != 6 {
		t.Fatalf("inventory: %+v", inventory)
	}
	want := []string{"mkdir /root", "mkdir /extras", "mount ro /dev/sda1 /root", "mount ro /dev/sda2 /root/boot", "mount ro /dev/vg/home /root/home", "mkdir /extras/" + filesystemID("/dev/sdb1"), "mount ro /dev/sdb1 /extras/" + filesystemID("/dev/sdb1")}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %v; want %v", f.calls, want)
	}
	for _, fs := range inventory.Filesystems {
		if fs.Extra != (fs.Device == "/dev/sdb1") {
			t.Fatalf("incorrect extra filesystem classification: %+v", fs)
		}
		if (fs.Type == "swap" || fs.Type == "LVM2_member") && fs.Mounted {
			t.Fatalf("container was mounted: %+v", fs)
		}
	}
}

func TestPrepareRefusesUnsafeGuests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fakeBackend)
	}{{"ambiguous", func(f *fakeBackend) { f.roots = append(f.roots, "other") }}, {"encrypted", func(f *fakeBackend) { f.files["/dev/sdd1"] = "crypto_LUKS" }}, {"non Linux", func(f *fakeBackend) { f.kind = "windows" }}, {"missing root", func(f *fakeBackend) { delete(f.points, "/") }}, {"bad mountpoint", func(f *fakeBackend) { f.points["/../etc"] = "/dev/sdb1" }}, {"failed mount", func(f *fakeBackend) { f.fail = "mount" }}} {
		t.Run(tc.name, func(t *testing.T) {
			h, f := fixture()
			tc.change(f)
			if _, err := h.Prepare(); err == nil {
				t.Fatal("unsafe guest accepted")
			}
		})
	}
}

func TestCloseRetainsProtectionOnFailure(t *testing.T) {
	for _, failure := range []string{"unmount", "sync", "shutdown"} {
		t.Run(failure, func(t *testing.T) {
			h, f := fixture()
			f.fail = failure
			if err := h.Close(); err == nil {
				t.Fatal("failure ignored")
			}
			if h.closed {
				t.Fatal("closed handle on failure")
			}
			for _, call := range f.calls {
				if call == "close" {
					t.Fatal("closed native handle after failed finalization")
				}
			}
			f.fail = ""
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			if !h.closed {
				t.Fatal("retry did not finalize")
			}
		})
	}
}

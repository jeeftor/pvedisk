package proxmox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEmbeddedHelperSyntax(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("Perl is unavailable")
	}
	cmd := exec.Command("perl", "-Itestdata", "-c", "helper.pl")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Perl syntax: %v: %s", err, out)
	}
}

func TestEmbeddedStoppedPreflightDoesNotChangeVM(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("Perl is unavailable")
	}
	for _, scenario := range []struct {
		name, config, message string
	}{
		{"stopped", `{}`, ""},
		{"owned-lock", `{"lock":"create"}`, ""},
		{"running", `{"test_running":true,"test_qmp":"running"}`, "qm shutdown 400"},
		{"paused", `{"test_running":true,"test_qmp":"paused"}`, "qm shutdown 400"},
		{"suspended", `{"vmstate":"saved"}`, "suspended"},
		{"unknown-monitor", `{"test_qmp":"unknown"}`, "paused or suspended"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(scenario.config), 0600); err != nil {
				t.Fatal(err)
			}
			// This read-only operation uses mocked PVE modules, so exercise it
			// without root on developer hosts as well as in native CI.
			script := strings.Replace(helper, `die "root required\n" if $> != 0;`, "", 1)
			cmd := exec.Command("perl", "-Itestdata", "-e", script, "--", "stopped", "400", "")
			cmd.Env = append(os.Environ(), "PVEDISK_TEST_CONFIG="+path)
			out, err := cmd.CombinedOutput()
			if scenario.message == "" {
				if err != nil || strings.TrimSpace(string(out)) != "[]" {
					t.Fatalf("stopped preflight: %v: %s", err, out)
				}
			} else if err == nil || !strings.Contains(string(out), scenario.message) {
				t.Fatalf("unsafe state accepted or unclear diagnostic: %v: %s", err, out)
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != scenario.config {
				t.Fatal("preflight changed VM configuration")
			}
		})
	}
}

func TestEmbeddedHelperLifecycle(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("ownership tests require root; run in Debian CI containers")
	}
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("Perl is unavailable")
	}
	for _, scenario := range []string{"attached-disks", "existing-lock", "pending", "ha", "paused", "running", "owned-release", "changed-config", "wrong-token", "stale-marker", "interrupted-acquire", "repeat-release", "changed-config-unlocked", "recreated-unlocked", "unlocked-running", "unlocked-wrong-token", "unlocked-invalid-marker", "unlocked-unsafe-marker"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, "state")
			disk := filepath.Join(dir, "disk.qcow2")
			if err := os.WriteFile(disk, nil, 0600); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(dir, "vm.json")
			conf := map[string]any{"scsi0": map[string]string{"file": "local:disk"}, "ide2": map[string]string{"file": "local:installer", "media": "cdrom"}, "scsi1": map[string]string{"file": "local:vm-400-cloudinit"}, "efidisk0": map[string]string{"file": "missing"}, "tpmstate0": map[string]string{"file": "missing"}, "unused0": map[string]string{"file": "missing"}}
			switch scenario {
			case "existing-lock":
				conf["lock"] = "backup"
			case "pending":
				conf["pending"] = map[string]string{"scsi0": "newdisk"}
			case "ha":
				conf["test_ha"] = true
			case "paused":
				conf["test_qmp"] = "paused"
				conf["test_running"] = true
			case "running":
				conf["test_qmp"] = "running"
				conf["test_running"] = true
			}
			writeConf := func() {
				t.Helper()
				data, err := json.Marshal(conf)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeConf()
			script := strings.ReplaceAll(helper, "/var/lib/pvedisk", base)
			run := func(operation, token string) ([]byte, error) {
				cmd := exec.Command("perl", "-Itestdata", "-e", script, "--", operation, "400", token)
				cmd.Env = append(os.Environ(), "PVEDISK_TEST_CONFIG="+configPath, "PVEDISK_TEST_DISK="+disk)
				return cmd.CombinedOutput()
			}
			token := "0123456789abcdef"
			out, err := run("acquire", token)
			switch scenario {
			case "existing-lock", "pending", "ha", "paused", "running":
				if err == nil {
					t.Fatalf("unsafe acquisition accepted: %s", out)
				}
				if _, err := os.Stat(base); !os.IsNotExist(err) {
					t.Fatalf("rejected operation created ownership state: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("acquire: %v: %s", err, out)
			}
			var disks []Disk
			if err := json.Unmarshal(out, &disks); err != nil {
				t.Fatal(err)
			}
			if len(disks) != 1 || disks[0].Path != disk || disks[0].Format != "qcow2" {
				t.Fatalf("disk selection: %s", out)
			}
			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &conf); err != nil {
				t.Fatal(err)
			}
			if conf["lock"] != "create" {
				t.Fatal("startup protection not acquired")
			}
			if scenario == "changed-config" {
				conf["name"] = "changed"
				writeConf()
			}
			if scenario == "interrupted-acquire" {
				delete(conf, "lock")
				writeConf()
			}
			if scenario == "wrong-token" {
				token = "fedcba9876543210"
			}
			if scenario == "stale-marker" {
				delete(conf, "lock")
				writeConf()
				out, err = run("acquire", token)
				if err == nil {
					t.Fatalf("stale marker accepted: %s", out)
				}
				return
			}
			unlocked := strings.Contains(scenario, "unlocked")
			ownerPath := filepath.Join(base, "locks", "400.json")
			if unlocked {
				delete(conf, "lock")
				conf["name"] = "changed after the session"
				switch scenario {
				case "recreated-unlocked":
					conf = map[string]any{"name": "replacement", "scsi0": map[string]string{"file": "another:disk", "format": "raw"}}
				case "unlocked-running":
					conf["test_running"] = true
				case "unlocked-wrong-token":
					token = "fedcba9876543210"
				case "unlocked-invalid-marker":
					if err := os.WriteFile(ownerPath, []byte(`{"token":"0123456789abcdef","digest":"invalid"}`), 0600); err != nil {
						t.Fatal(err)
					}
				case "unlocked-unsafe-marker":
					if err := os.Chmod(ownerPath, 0644); err != nil {
						t.Fatal(err)
					}
				}
				writeConf()
			}
			configBefore, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			ownerBefore, err := os.ReadFile(ownerPath)
			if err != nil {
				t.Fatal(err)
			}
			out, err = run("release", token)
			if unlocked {
				wantFailure := strings.HasPrefix(scenario, "unlocked-")
				if (err != nil) != wantFailure {
					t.Fatalf("unlocked cleanup failure=%t: %v: %s", wantFailure, err, out)
				}
				configAfter, readErr := os.ReadFile(configPath)
				if readErr != nil || string(configAfter) != string(configBefore) {
					t.Fatalf("unlocked VM configuration changed: %v", readErr)
				}
				ownerAfter, readErr := os.ReadFile(ownerPath)
				if wantFailure {
					if readErr != nil || string(ownerAfter) != string(ownerBefore) {
						t.Fatalf("unsafe cleanup changed ownership marker: %v", readErr)
					}
				} else if !os.IsNotExist(readErr) {
					t.Fatalf("stale owned marker remains: %v", readErr)
				}
				return
			}
			if scenario == "changed-config" || scenario == "wrong-token" {
				if err == nil {
					t.Fatal("uncertain ownership unlocked")
				}
				data, _ := os.ReadFile(configPath)
				if !strings.Contains(string(data), `"lock":"create"`) {
					t.Fatal("protection was removed")
				}
				return
			}
			if err != nil {
				t.Fatalf("release: %v: %s", err, out)
			}
			if scenario == "repeat-release" {
				if out, err := run("release", token); err != nil {
					t.Fatalf("idempotent release: %v: %s", err, out)
				}
			}
			data, _ = os.ReadFile(configPath)
			if strings.Contains(string(data), `"lock"`) {
				t.Fatal("owned lock retained after cleanup")
			}
			if _, err := os.Stat(filepath.Join(base, "locks", "400.json")); !os.IsNotExist(err) {
				t.Fatalf("ownership record remains: %v", err)
			}
		})
	}
}

func TestEmbeddedHelperListsOnlyLocalMetadata(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("embedded helper requires root; run in Debian CI containers")
	}
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("Perl is unavailable")
	}
	conf := map[string]any{
		"400": map[string]any{"name": "linux", "scsi0": "deliberately invalid disk metadata"},
		"401": map[string]any{"name": "web", "test_running": true},
		"402": map[string]any{"name": "backup", "lock": "backup"},
		"403": map[string]any{"name": "ha", "test_ha": true},
		"404": map[string]any{"name": "sleeping", "lock": "suspended", "vmstate": "saved-state"},
		"405": map[string]any{"name": "base", "template": true},
		"406": map[string]any{"name": "pending", "pending": map[string]string{"scsi0": "new-disk"}},
		"407": map[string]any{"name": "paused", "test_running": true, "test_qmp": "paused"},
		"408": map[string]any{"name": "unknown-monitor", "test_running": true, "test_qmp": ""},
		"409": map[string]any{"name": "remote", "test_remote": true},
		"410": map[string]any{},
	}
	path := filepath.Join(t.TempDir(), "vms.json")
	run := func() ([]byte, error) {
		cmd := exec.Command("perl", "-Itestdata", "-e", helper, "--", "list")
		cmd.Env = append(os.Environ(), "PVEDISK_TEST_LIST=1", "PVEDISK_TEST_CONFIG="+path)
		return cmd.CombinedOutput()
	}
	write := func() []byte {
		t.Helper()
		data, err := json.Marshal(conf)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := write()
	out, err := run()
	if err != nil {
		t.Fatalf("list local metadata: %v: %s", err, out)
	}
	var got []VM
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := []VM{
		{ID: 400, Name: "linux", Status: "stopped"},
		{ID: 401, Name: "web", Status: "running"},
		{ID: 402, Name: "backup", Status: "stopped", UnavailableReason: "Proxmox lock: backup"},
		{ID: 403, Name: "ha", Status: "stopped", UnavailableReason: "HA-managed"},
		{ID: 404, Name: "sleeping", Status: "stopped", UnavailableReason: "Proxmox lock: suspended; suspended state"},
		{ID: 405, Name: "base", Status: "stopped", UnavailableReason: "template"},
		{ID: 406, Name: "pending", Status: "stopped", UnavailableReason: "pending configuration changes"},
		{ID: 407, Name: "paused", Status: "paused", UnavailableReason: "VM is paused"},
		{ID: 408, Name: "unknown-monitor", Status: "running", UnavailableReason: "monitor status unavailable"},
		{ID: 410, Name: "VM 410", Status: "stopped"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("VM listing:\ngot  %#v\nwant %#v", got, want)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("listing changed configuration: %v", err)
	}
	conf = map[string]any{"400": map[string]any{"test_status": "unsupported"}}
	write()
	if out, err := run(); err == nil || !strings.Contains(string(out), "cannot determine status") {
		t.Fatalf("unsupported status accepted: %v: %s", err, out)
	}
	conf = map[string]any{}
	write()
	if out, err := run(); err != nil || strings.TrimSpace(string(out)) != "[]" {
		t.Fatalf("empty inventory: %v: %s", err, out)
	}
}

func TestEmbeddedHelperReleasesDeletedVM(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("ownership tests require root; run in Debian CI containers")
	}
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("Perl is unavailable")
	}
	for _, scenario := range []string{"deleted", "empty-cluster", "repeat-release", "wrong-token", "invalid-marker", "unsafe-directory", "remote-config", "recreated", "running", "cluster-unmounted", "no-quorum", "cluster-error", "bad-inventory", "config-parse-error"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, "state")
			configPath := filepath.Join(dir, "vm.json")
			ownerPath := filepath.Join(base, "locks", "400.json")
			diskPath := filepath.Join(dir, "disk")
			for path, data := range map[string]string{configPath: `{"scsi0":{"file":"local:disk","format":"raw"}}`, diskPath: ""} {
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			token := "0123456789abcdef"
			extraEnv := []string{}
			script := strings.ReplaceAll(helper, "/var/lib/pvedisk", base)
			run := func(operation string) ([]byte, error) {
				cmd := exec.Command("perl", "-Itestdata", "-e", script, "--", operation, "400", token)
				cmd.Env = append(os.Environ(), "PVEDISK_TEST_CONFIG="+configPath, "PVEDISK_TEST_DISK="+diskPath)
				cmd.Env = append(cmd.Env, extraEnv...)
				return cmd.CombinedOutput()
			}
			if out, err := run("acquire"); err != nil {
				t.Fatalf("acquire: %v: %s", err, out)
			}
			if err := os.Remove(configPath); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "wrong-token":
				token = "fedcba9876543210"
			case "empty-cluster":
				extraEnv = []string{"PVEDISK_TEST_EMPTY_CLUSTER=1"}
			case "invalid-marker":
				if err := os.WriteFile(ownerPath, []byte(`{"token":"0123456789abcdef"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unsafe-directory":
				if err := os.Chmod(filepath.Dir(ownerPath), 0755); err != nil {
					t.Fatal(err)
				}
			case "remote-config":
				extraEnv = []string{"PVEDISK_TEST_REMOTE=1"}
			case "recreated":
				extraEnv = []string{"PVEDISK_TEST_RECREATE=1"}
			case "running":
				extraEnv = []string{"PVEDISK_TEST_RUNNING=1"}
			case "cluster-unmounted":
				extraEnv = []string{"PVEDISK_TEST_CFS_UNMOUNTED=1"}
			case "no-quorum":
				extraEnv = []string{"PVEDISK_TEST_NO_QUORUM=1"}
			case "cluster-error":
				extraEnv = []string{"PVEDISK_TEST_CFS_ERROR=1"}
			case "bad-inventory":
				extraEnv = []string{"PVEDISK_TEST_BAD_INVENTORY=1"}
			case "config-parse-error":
				if err := os.WriteFile(configPath, []byte("broken configuration"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(ownerPath)
			if err != nil {
				t.Fatal(err)
			}
			out, err := run("release")
			if scenario == "deleted" || scenario == "empty-cluster" || scenario == "repeat-release" {
				if err != nil {
					t.Fatalf("release deleted VM: %v: %s", err, out)
				}
				if _, err := os.Stat(ownerPath); !os.IsNotExist(err) {
					t.Fatalf("owned marker remains: %v", err)
				}
				if _, err := os.Stat(configPath); !os.IsNotExist(err) {
					t.Fatalf("deleted configuration was recreated: %v", err)
				}
				if scenario == "repeat-release" {
					if out, err := run("release"); err != nil {
						t.Fatalf("repeat deleted VM release: %v: %s", err, out)
					}
				}
				return
			}
			if err == nil {
				t.Fatalf("unsafe release accepted: %s", out)
			}
			after, err := os.ReadFile(ownerPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("uncertain owner marker changed: %v", err)
			}
			if scenario == "recreated" {
				data, err := os.ReadFile(configPath)
				if err != nil || string(data) != `{"name":"recreated","lock":"backup"}` {
					t.Fatalf("recreated VM configuration changed: %s: %v", data, err)
				}
			}
		})
	}
}

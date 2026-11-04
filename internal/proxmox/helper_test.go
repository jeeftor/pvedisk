package proxmox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

func TestEmbeddedHelperLifecycle(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("ownership tests require root; run in Debian CI containers")
	}
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("Perl is unavailable")
	}
	for _, scenario := range []string{"attached-disks", "existing-lock", "pending", "ha", "paused", "running", "owned-release", "changed-config", "wrong-token", "stale-marker", "interrupted-acquire", "repeat-release"} {
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
			out, err = run("release", token)
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

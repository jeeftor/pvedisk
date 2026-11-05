package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeeftor/pvedisk/internal/appliance"
	"github.com/jeeftor/pvedisk/internal/proxmox"
	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixtureDescription() session.Description {
	return session.Description{VMID: 400, State: &session.State{VMID: 400, Token: strings.Repeat("a", 64), PasswordFile: "/secret/password-file", Staging: "/var/lib/pvedisk/vm400-stage", Phase: "mounted", Public: "/repair/vm400", Extras: "/repair/vm400-extra", ReadOnly: true, Worker: session.Identity{PID: 123, Start: "private-process-start", Boot: "private-boot"}, Inventory: appliance.Inventory{Filesystems: []appliance.Filesystem{{Device: "/dev/sda1", Type: "ext4", Mountpoint: "/", Mounted: true}, {Device: "/dev/sda2", Type: "ext4", Mountpoint: "/home", Mounted: true}, {Device: "/dev/sdb1", Type: "xfs", ID: "fs-extra", Mountpoint: "/extras/fs-extra", Mounted: true, Extra: true}, {Device: "/dev/sda3", Type: "swap"}}}}, Observation: session.Observation{Status: "mounted", MountsKnown: true, WorkerKnown: true, WorkerAlive: true, RootMounted: true, ExtrasMounted: true, StagingMounted: true}}
}

func fixtureOps(t *testing.T) operations {
	t.Helper()
	return operations{
		listVMs: func(context.Context) ([]proxmox.VM, error) {
			return []proxmox.VM{{ID: 400, Name: "linux", Status: "stopped"}}, nil
		},
		listSessions: func() ([]session.Description, error) { return []session.Description{fixtureDescription()}, nil },
		describe: func(id int) (*session.Description, error) {
			row := fixtureDescription()
			row.VMID = id
			return &row, nil
		},
		doctor:  func(context.Context) error { return nil },
		mount:   func(context.Context, int, string, bool, func(string)) error { t.Error("unexpected mount"); return nil },
		unmount: func(context.Context, int) error { t.Error("unexpected unmount"); return nil },
		clean:   func(context.Context, int) error { t.Error("unexpected cleanup"); return nil },
	}
}

func clientFor(t *testing.T, s *mcp.Server, options *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	a, b := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, a, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, options)
	cs, err := c.Connect(ctx, b, nil)
	if err != nil {
		cancel()
		ss.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close(); cancel() })
	return cs
}

func call(t *testing.T, c *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	r, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s transport error: %v", name, err)
	}
	return r
}

func resultJSON(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestToolsAdvertiseStrictContracts(t *testing.T) {
	c := clientFor(t, newServer("/missing-config", fixtureOps(t)), nil)
	list, err := c.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readonly := map[string]bool{"version": true, "list_vms": true, "list_sessions": true, "status": true, "ps": true, "doctor": true, "mount": false, "unmount": false, "clean": false}
	if len(list.Tools) != len(readonly) {
		t.Fatalf("unexpected tools: %v", list.Tools)
	}
	for _, tool := range list.Tools {
		ro, ok := readonly[tool.Name]
		if !ok {
			t.Fatalf("unexpected tool %s", tool.Name)
		}
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint != ro || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint == ro {
			t.Fatalf("incorrect annotations: %+v", tool)
		}
		data, _ := json.Marshal(tool.InputSchema)
		var schema map[string]any
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("schema permits unexpected arguments: %s", data)
		}
		if tool.OutputSchema == nil {
			t.Fatalf("%s has no typed output schema", tool.Name)
		}
		if tool.Name == "mount" {
			mode := schema["properties"].(map[string]any)["read_only"].(map[string]any)
			if mode["default"] != false {
				t.Fatal("mount default is not writable")
			}
		}
	}
}

func TestReadOnlyToolsDoNotLoadConfigOrMutate(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "absent", "config.yaml")
	ops := fixtureOps(t)
	c := clientFor(t, newServer(configPath, ops), nil)
	for _, name := range []string{"version", "list_vms", "list_sessions", "ps", "doctor", "status"} {
		args := map[string]any{}
		if name == "status" {
			args["vmid"] = 400
		}
		result := call(t, c, name, args)
		if result.IsError {
			t.Fatalf("%s: %s", name, resultJSON(t, result))
		}
	}
	if _, err := os.Stat(filepath.Dir(configPath)); !os.IsNotExist(err) {
		t.Fatalf("read-only tools created configuration directory: %v", err)
	}
}

func TestInvalidInputsNeverInvokeOperations(t *testing.T) {
	ops := fixtureOps(t)
	ops.describe = func(int) (*session.Description, error) { t.Error("invalid input reached status"); return nil, nil }
	c := clientFor(t, newServer("/config", ops), nil)
	for _, name := range []string{"status", "mount", "unmount", "clean"} {
		for _, args := range []map[string]any{{}, {"vmid": 99}, {"vmid": 1000000000}, {"vmid": "400"}, {"vmid": 400, "shell": "never-run"}, {"vmid": 400, "config": "/alternate"}} {
			if result := call(t, c, name, args); !result.IsError {
				t.Fatalf("%s accepted %v", name, args)
			}
		}
	}
	if result := call(t, c, "mount", map[string]any{"vmid": 400, "read_only": "true"}); !result.IsError {
		t.Fatal("accepted nonboolean read_only")
	}
}

func TestPublicSessionRedactionAndHostPaths(t *testing.T) {
	row := fixtureDescription()
	row.Error = "error " + row.State.Token
	row.State.Error = "cannot remove " + row.State.PasswordFile
	row.Observation.Error = "control /var/lib/pvedisk/400.sock log /var/lib/pvedisk/400.log"
	ops := fixtureOps(t)
	ops.listSessions = func() ([]session.Description, error) { return []session.Description{row}, nil }
	ops.describe = func(int) (*session.Description, error) { return &row, nil }
	c := clientFor(t, newServer("/config", ops), nil)
	for _, name := range []string{"list_sessions", "status"} {
		args := map[string]any{}
		if name == "status" {
			args["vmid"] = 400
		}
		r := call(t, c, name, args)
		if r.IsError {
			t.Fatal(resultJSON(t, r))
		}
		text := resultJSON(t, r)
		for _, secret := range []string{row.State.Token, row.State.PasswordFile, "private-process-start", "private-boot", "/var/lib/pvedisk", "password_file", "\"token\"", "\"staging\""} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s exposed %s: %s", name, secret, text)
			}
		}
		for _, public := range []string{"/repair/vm400/home", "/repair/vm400-extra/fs-extra", "/dev/sda3", "read-only"} {
			if !strings.Contains(text, public) {
				t.Fatalf("%s missing public detail %s", name, public)
			}
		}
	}
}

func TestMountUsesCapturedConfigModeAndProgress(t *testing.T) {
	ops := fixtureOps(t)
	var modes []bool
	ops.mount = func(_ context.Context, id int, path string, ro bool, progress func(string)) error {
		if id != 400 || path != "/site/pvedisk.yaml" {
			t.Fatalf("wrong mount arguments %d %s", id, path)
		}
		modes = append(modes, ro)
		progress("shutting-down")
		progress("mounted")
		return nil
	}
	progress := make(chan *mcp.ProgressNotificationParams, 4)
	c := clientFor(t, newServer("/site/pvedisk.yaml", ops), &mcp.ClientOptions{ProgressNotificationHandler: func(_ context.Context, request *mcp.ProgressNotificationClientRequest) { progress <- request.Params }})
	params := &mcp.CallToolParams{Name: "mount", Arguments: map[string]any{"vmid": 400}}
	params.SetProgressToken("mount-progress")
	r, err := c.CallTool(context.Background(), params)
	if err != nil || r.IsError {
		t.Fatalf("mount failed %v %v", r, err)
	}
	for i := 1; i <= 2; i++ {
		select {
		case p := <-progress:
			if p.ProgressToken != "mount-progress" || p.Progress != float64(i) {
				t.Fatalf("bad progress %+v", p)
			}
		case <-time.After(time.Second):
			t.Fatal("no MCP progress")
		}
	}
	r = call(t, c, "mount", map[string]any{"vmid": 400, "read_only": true})
	if r.IsError {
		t.Fatal(resultJSON(t, r))
	}
	if len(modes) != 2 || modes[0] || !modes[1] {
		t.Fatalf("wrong mount modes %v", modes)
	}
	select {
	case <-progress:
		t.Fatal("progress sent without a requested token")
	default:
	}
}

func TestToolFailuresAreToolErrorsAndRedacted(t *testing.T) {
	ops := fixtureOps(t)
	ops.mount = func(context.Context, int, string, bool, func(string)) error {
		return errors.New("private /var/lib/pvedisk/.password-" + strings.Repeat("a", 64))
	}
	ops.doctor = func(context.Context) error { return errors.New("runtime missing") }
	ops.unmount = func(context.Context, int) error { return errors.New("busy mount") }
	ops.clean = func(context.Context, int) error { return errors.New("ownership uncertain") }
	c := clientFor(t, newServer("/config", ops), nil)
	for _, name := range []string{"mount", "doctor", "unmount", "clean"} {
		args := map[string]any{}
		if name != "doctor" {
			args["vmid"] = 400
		}
		r := call(t, c, name, args)
		if !r.IsError {
			t.Fatalf("%s failure lost", name)
		}
		text := resultJSON(t, r)
		if strings.Contains(text, "/var/lib/pvedisk") || strings.Contains(text, strings.Repeat("a", 64)) {
			t.Fatal("private error exposed")
		}
		if name == "mount" && !strings.Contains(text, "inspect status before retrying") {
			t.Fatal("missing detached-worker retry guidance")
		}
	}
}

func TestCleanupDoesNotAdviseStartingDeletedOrRebuiltVM(t *testing.T) {
	ops := fixtureOps(t)
	ops.listVMs = func(context.Context) ([]proxmox.VM, error) {
		t.Fatal("cleanup must not depend on current VM inventory")
		return nil, nil
	}
	ops.unmount = func(context.Context, int) error { return nil }
	ops.clean = func(context.Context, int) error { return nil }
	c := clientFor(t, newServer("/missing-config", ops), nil)
	for _, name := range []string{"unmount", "clean"} {
		r := call(t, c, name, map[string]any{"vmid": 400})
		text := resultJSON(t, r)
		if r.IsError || strings.Contains(text, "qm start") || !strings.Contains(text, "never started") {
			t.Fatalf("unsafe cleanup advice: %s", text)
		}
	}
}

func TestRecordedInventoryDoesNotClaimCurrentMounts(t *testing.T) {
	for _, observation := range []session.Observation{{Status: "not-mounted", MountsKnown: true, WorkerKnown: true}, {Status: "unknown"}, {Status: "conflict", MountsKnown: true, RootMounted: true, ExtrasMounted: true, WorkerKnown: true, WorkerAlive: true}, {Status: "partial", MountsKnown: true, RootMounted: true, ExtrasMounted: true, WorkerKnown: true, WorkerAlive: false}} {
		row := fixtureDescription()
		row.Observation = observation
		out := publicSession(row)
		for _, fs := range out.Filesystems {
			if fs.Mounted {
				t.Fatalf("saved inventory claimed a live mount: %+v", out)
			}
		}
		if !out.Filesystems[0].GuestMountedAtPrepare || out.Filesystems[0].HostPath != "/repair/vm400" {
			t.Fatal("lost recorded filesystem location")
		}
	}
}

func TestMutationGatesSerializeAndHonorCancellation(t *testing.T) {
	g := &vmGates{entries: make(map[int]*vmGate)}
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- g.run(context.Background(), 400, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	if err := g.run(ctx, 400, func() error { calls.Add(1); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled gate: %v", err)
	}
	if err := g.run(context.Background(), 401, func() error { calls.Add(1); return nil }); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("same-VM canceled operation executed")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if len(g.entries) != 0 {
		t.Fatal("completed gates retained")
	}
	if err := g.run(ctx, 400, func() error { t.Fatal("already canceled operation executed"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

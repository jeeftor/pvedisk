// Package mcpserver exposes pvedisk lifecycle operations through MCP tools.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jeeftor/pvedisk/internal/buildinfo"
	"github.com/jeeftor/pvedisk/internal/proxmox"
	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type operations struct {
	listVMs      func(context.Context) ([]proxmox.VM, error)
	listSessions func() ([]session.Description, error)
	describe     func(int) (*session.Description, error)
	doctor       func(context.Context) error
	mount        func(context.Context, int, string, bool, func(string)) error
	unmount      func(context.Context, int) error
	clean        func(context.Context, int) error
}

// New constructs tools using shared lifecycle managers and one fixed config path.
// Read-only tools, unmount, and cleanup never load site configuration.
func New(configPath string) *mcp.Server {
	m := session.New()
	p := proxmox.New()
	return newServer(configPath, operations{listVMs: p.List, listSessions: m.List, describe: m.Describe, doctor: p.Doctor, mount: m.Mount, unmount: m.Unmount, clean: m.Clean})
}

type emptyInput struct{}
type vmInput struct {
	VMID int `json:"vmid"`
}
type mountInput struct {
	VMID     int  `json:"vmid"`
	ReadOnly bool `json:"read_only,omitempty"`
}
type vmList struct {
	VMs []proxmox.VM `json:"vms"`
}
type sessionList struct {
	Sessions []PublicSession `json:"sessions"`
}
type diagnosticResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}
type operationResult struct {
	VMID    int            `json:"vmid"`
	Message string         `json:"message"`
	Session *PublicSession `json:"session,omitempty"`
}
type worker struct {
	VMID        int    `json:"vmid"`
	PID         int    `json:"pid"`
	Mode        string `json:"mode"`
	MountStatus string `json:"mount_status"`
	Path        string `json:"path"`
}
type processList struct {
	Workers          []worker `json:"workers"`
	InactiveRecords  int      `json:"inactive_records"`
	UncertainRecords int      `json:"uncertain_records"`
}

func schema(withID, withMode bool) map[string]any {
	properties := map[string]any{}
	required := []string{}
	if withID {
		properties["vmid"] = map[string]any{"type": "integer", "minimum": 100, "maximum": 999999999}
		required = append(required, "vmid")
	}
	if withMode {
		properties["read_only"] = map[string]any{"type": "boolean", "default": false}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func tool(name, description string, readonly, withID, withMode bool) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: description, InputSchema: schema(withID, withMode), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readonly, DestructiveHint: boolPointer(!readonly), OpenWorldHint: boolPointer(false)}}
}

func boolPointer(value bool) *bool { return &value }

func newServer(configPath string, ops operations) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "pvedisk", Version: buildinfo.Version}, &mcp.ServerOptions{Instructions: "Manage local Proxmox repair sessions. Mount requires a stopped VM and grants disk access. Mount automatically reconciles stale sessions with exited workers, retaining protection if cleanup is uncertain. Never start a VM before cleanup succeeds. If a mount call is canceled or interrupted, its detached worker may continue: inspect status before retrying. Public filesystem host paths are on this Proxmox host."})
	gates := &vmGates{entries: make(map[int]*vmGate)}
	mcp.AddTool(s, tool("version", "Read the running pvedisk executable build metadata.", true, false, false), func(context.Context, *mcp.CallToolRequest, emptyInput) (*mcp.CallToolResult, buildinfo.Info, error) {
		return nil, buildinfo.Current(), nil
	})
	mcp.AddTool(s, tool("list_vms", "List local Proxmox VMs and lifecycle restrictions without inspecting disks or changing VM state.", true, false, false), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, vmList, error) {
		vms, err := ops.listVMs(ctx)
		if vms == nil {
			vms = make([]proxmox.VM, 0)
		}
		return nil, vmList{VMs: vms}, safeError(err)
	})
	mcp.AddTool(s, tool("list_sessions", "List saved repair sessions with current mount and worker observations; no site configuration required.", true, false, false), func(context.Context, *mcp.CallToolRequest, emptyInput) (*mcp.CallToolResult, sessionList, error) {
		rows, err := ops.listSessions()
		out := sessionList{Sessions: make([]PublicSession, 0, len(rows))}
		for _, row := range rows {
			out.Sessions = append(out.Sessions, publicSession(row))
		}
		return nil, out, safeError(err)
	})
	mcp.AddTool(s, tool("status", "Read one recorded repair session and its actual mount/worker status, without changing state.", true, true, false), func(_ context.Context, _ *mcp.CallToolRequest, in vmInput) (*mcp.CallToolResult, PublicSession, error) {
		if err := validID(in.VMID); err != nil {
			return nil, PublicSession{}, err
		}
		row, err := ops.describe(in.VMID)
		if err != nil {
			return nil, PublicSession{}, safeError(err)
		}
		if row == nil {
			return nil, PublicSession{}, errors.New("session status unavailable")
		}
		return nil, publicSession(*row), nil
	})
	mcp.AddTool(s, tool("ps", "List verified running pvedisk workers; also count inactive and uncertain saved records.", true, false, false), func(context.Context, *mcp.CallToolRequest, emptyInput) (*mcp.CallToolResult, processList, error) {
		rows, err := ops.listSessions()
		out := processList{Workers: make([]worker, 0)}
		for _, row := range rows {
			state := row.State
			if state == nil || row.Error != "" || !row.Observation.WorkerKnown || validID(row.VMID) != nil || state.VMID != row.VMID {
				out.UncertainRecords++
				continue
			}
			if !row.Observation.WorkerAlive {
				out.InactiveRecords++
				continue
			}
			if state.Worker.PID <= 0 {
				out.UncertainRecords++
				continue
			}
			mode := "read/write"
			if state.ReadOnly {
				mode = "read-only"
			}
			status := row.Observation.Status
			if status == "" {
				status = "unknown"
			}
			out.Workers = append(out.Workers, worker{VMID: row.VMID, PID: state.Worker.PID, Mode: mode, MountStatus: status, Path: state.Public})
		}
		return nil, out, safeError(err)
	})
	mcp.AddTool(s, tool("doctor", "Check local Proxmox runtime tools, modules, and FUSE access without changing VM state.", true, false, false), func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, diagnosticResult, error) {
		err := ops.doctor(ctx)
		return nil, diagnosticResult{OK: err == nil, Message: "Runtime checks passed."}, safeError(err)
	})
	mcp.AddTool(s, tool("mount", "Mount an already stopped VM writable by default. Safely reconcile its dead-worker session, acquire startup protection, and start a detached worker. On cancellation the worker may continue: inspect status before retrying.", false, true, true), func(ctx context.Context, req *mcp.CallToolRequest, in mountInput) (*mcp.CallToolResult, operationResult, error) {
		out := operationResult{VMID: in.VMID}
		err := gates.run(ctx, in.VMID, func() error {
			var progress float64
			notify := func(phase string) {
				if req.Params.GetProgressToken() != nil {
					progress++
					_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: req.Params.GetProgressToken(), Progress: progress, Message: publicError(phase, nil)})
				}
			}
			if err := ops.mount(ctx, in.VMID, configPath, in.ReadOnly, notify); err != nil {
				return fmt.Errorf("mount failed or interrupted; a detached worker may continue, inspect status before retrying: %w", safeError(err))
			}
			row, err := ops.describe(in.VMID)
			if err != nil {
				return fmt.Errorf("mount completed but status is unavailable; inspect status before retrying: %w", safeError(err))
			}
			if row == nil {
				return errors.New("mount completed but status is unavailable; inspect status before retrying")
			}
			public := publicSession(*row)
			out.Session = &public
			out.Message = "Your VM is mounted. Use unmount when finished."
			return nil
		})
		return nil, out, safeError(err)
	})
	for _, operation := range []struct {
		name, description string
		run               func(context.Context, int) error
	}{{"unmount", "Unmount public views and finalize guest writes before releasing owned startup protection. No configuration is loaded.", ops.unmount}, {"clean", "Reconcile an interrupted session using recorded paths. Retain protection when ownership, disk users, or cleanup are uncertain. No configuration is loaded.", ops.clean}} {
		mcp.AddTool(s, tool(operation.name, operation.description, false, true, false), func(ctx context.Context, _ *mcp.CallToolRequest, in vmInput) (*mcp.CallToolResult, operationResult, error) {
			err := gates.run(ctx, in.VMID, func() error { return operation.run(ctx, in.VMID) })
			if err != nil {
				return nil, operationResult{}, safeError(err)
			}
			return nil, operationResult{VMID: in.VMID, Message: fmt.Sprintf("Cleanup for VM %d completed; your guest was never started.", in.VMID)}, nil
		})
	}
	return s
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(publicError(err.Error(), nil))
}
func validID(id int) error {
	if id < 100 || id > 999999999 {
		return errors.New("VM identifier must be between 100 and 999999999")
	}
	return nil
}

type vmGate struct {
	busy chan struct{}
	refs int
}
type vmGates struct {
	mu      sync.Mutex
	entries map[int]*vmGate
}

func (g *vmGates) run(ctx context.Context, id int, fn func() error) error {
	if err := validID(id); err != nil {
		return err
	}
	g.mu.Lock()
	entry := g.entries[id]
	if entry == nil {
		entry = &vmGate{busy: make(chan struct{}, 1)}
		g.entries[id] = entry
	}
	entry.refs++
	g.mu.Unlock()
	drop := func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		entry.refs--
		if entry.refs == 0 {
			delete(g.entries, id)
		}
	}
	select {
	case entry.busy <- struct{}{}:
		defer func() { <-entry.busy; drop() }()
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	case <-ctx.Done():
		drop()
		return ctx.Err()
	}
}

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/jeeftor/pvedisk/internal/proxmox"
	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/spf13/cobra"
)

func selectMountVM(cmd *cobra.Command, ops operations) (int, error) {
	vms, err := ops.listVMs(cmd.Context())
	if err != nil {
		return 0, fmt.Errorf("list your local VMs: %w", err)
	}
	sessions, err := ops.listSessions()
	if err != nil {
		return 0, fmt.Errorf("check your repair sessions: %w", err)
	}
	if len(vms) == 0 {
		return 0, errors.New("your node has no local VMs to mount")
	}
	known := make(map[int]string)
	for _, description := range sessions {
		if description.Error != "" && description.VMID == 0 {
			return 0, errors.New("your repair state contains an unreadable record; inspect pvedisk status before mounting")
		}
		known[description.VMID] = "existing repair session; inspect pvedisk status"
	}
	eligible := make(map[int]bool)
	var rows [][]string
	for _, vm := range vms {
		reason := vm.UnavailableReason
		if own, exists := known[vm.ID]; exists {
			reason = own
		}
		availability := "available"
		if reason != "" {
			availability = reason
		} else {
			eligible[vm.ID] = true
		}
		rows = append(rows, []string{strconv.Itoa(vm.ID), vm.Name, vm.Status, availability})
	}
	if err := printTable(cmd.OutOrStdout(), "Your local VMs", []string{"VMID", "NAME", "VM STATE", "REPAIR AVAILABILITY"}, rows); err != nil {
		return 0, err
	}
	if len(eligible) == 0 {
		return 0, errors.New("your local VMs have no available repair targets; inspect the reasons above")
	}
	return promptVMID(cmd, eligible)
}

func selectUnmountVM(cmd *cobra.Command, ops operations) (int, error) {
	sessions, err := ops.listSessions()
	if err != nil {
		return 0, fmt.Errorf("list your repair sessions: %w", err)
	}
	vms, inventoryErr := ops.listVMs(cmd.Context())
	if err := printSessions(cmd.OutOrStdout(), sessions, vms, inventoryErr); err != nil {
		return 0, err
	}
	eligible := make(map[int]bool)
	for _, description := range sessions {
		if canUnmount(description) {
			eligible[description.VMID] = true
		}
	}
	if len(eligible) == 0 {
		return 0, errors.New("you have no mounted sessions available for unmount; incomplete sessions require pvedisk recover VMID")
	}
	return promptVMID(cmd, eligible)
}

func canUnmount(description session.Description) bool {
	if description.State == nil || description.Error != "" || !description.Observation.WorkerAlive {
		return false
	}
	switch description.Observation.Status {
	case "mounted", "partial":
		return true
	case "not-mounted":
		switch description.State.Phase {
		case "failed", "finalizing", "finalized":
			return true
		}
	}
	return false
}

func printSessions(output io.Writer, sessions []session.Description, vms []proxmox.VM, inventoryErr error) error {
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(output, "You have no recorded pvedisk repair sessions. Mounts created by other tools are not tracked.")
		return err
	}
	localVMs := make(map[int]bool, len(vms))
	for _, vm := range vms {
		localVMs[vm.ID] = true
	}
	var rows [][]string
	var details []string
	for _, description := range sessions {
		id := strconv.Itoa(description.VMID)
		mode, phase := "unknown", "unreadable"
		if state := description.State; state != nil {
			mode, phase = "read/write", state.Phase
			if state.ReadOnly {
				mode = "read-only"
			}
		}
		next := "pvedisk recover " + id
		if canUnmount(description) {
			next = "pvedisk unmount " + id
		}
		if description.VMID == 0 {
			id, next = "invalid", "inspect saved record"
		}
		disks := diskStatus(description)
		if description.Error != "" {
			disks = "unreadable"
		}
		rows = append(rows, []string{id, disks, phase, next})
		details = append(details, "VM "+id+" · saved mode: "+mode)
		if description.State != nil {
			label := "  Saved root directory: "
			if description.Observation.RootMounted && description.Observation.Status != "conflict" {
				label = "  Mounted root: "
			}
			details = append(details, label+description.State.Public)
		}
		if description.VMID == 0 {
			details = append(details, "  Saved record: "+description.Record)
		}
		if description.Error != "" {
			details = append(details, "  Saved record error: "+description.Error)
		}
		if description.Observation.Error != "" {
			details = append(details, "  Current mount check: "+description.Observation.Error)
		}
		if description.State != nil && description.State.Error != "" {
			details = append(details, "  Saved error: "+description.State.Error)
		}
		if inventoryErr == nil && description.VMID != 0 && !localVMs[description.VMID] {
			details = append(details, "  VM not found on this node; it may have been deleted or moved.")
		}
		details = append(details, "")
	}
	if err := printTable(output, "Your mounted disks and sessions awaiting cleanup", []string{"VMID", "DISKS", "SAVED STEP", "NEXT STEP"}, rows); err != nil {
		return err
	}
	fmt.Fprintln(output)
	for _, line := range details {
		if _, err := fmt.Fprintln(output, cleanDisplay(line)); err != nil {
			return err
		}
	}
	p := outputPalette(output)
	if inventoryErr != nil {
		fmt.Fprintln(output, p.warning.Render("VM availability is unknown: "+cleanDisplay(inventoryErr.Error())+". Your saved sessions are still shown."))
	}
	_, err := fmt.Fprintln(output, p.muted.Render("Saved steps and errors describe earlier work, not current mounts. Sessions remain until cleanup succeeds. Complete cleanup before starting your guests."))
	return err
}

func cleanDisplay(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

func diskStatus(description session.Description) string {
	switch description.Observation.Status {
	case "mounted":
		return "mounted"
	case "partial":
		return "partly mounted"
	case "not-mounted":
		return "not mounted"
	case "conflict":
		return "mount conflict"
	default:
		return "unknown"
	}
}

func promptVMID(cmd *cobra.Command, eligible map[int]bool) (int, error) {
	p := outputPalette(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), p.muted.Render("Enter an available VM ID, or press Enter to cancel."))
	fmt.Fprint(cmd.OutOrStdout(), p.label.Render("VM ID:")+" ")
	input, err := bufio.NewReader(io.LimitReader(cmd.InOrStdin(), 65)).ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("read your VM ID; selection cancelled: %w", err)
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, errors.New("your VM selection was cancelled")
	}
	id, err := strconv.Atoi(input)
	if err != nil || !eligible[id] {
		return 0, errors.New("choose an available VM ID from your displayed list")
	}
	return id, nil
}

func printTable(output io.Writer, title string, headers []string, rows [][]string) error {
	_, err := fmt.Fprint(output, outputPalette(output).table(title, headers, rows))
	return err
}

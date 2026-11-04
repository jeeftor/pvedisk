package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"

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
	if err := printSessions(cmd.OutOrStdout(), sessions); err != nil {
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

func printSessions(output io.Writer, sessions []session.Description) error {
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(output, "You have no recorded pvedisk repair sessions. Mounts created by other tools are not tracked.")
		return err
	}
	var rows [][]string
	for _, description := range sessions {
		id := strconv.Itoa(description.VMID)
		mode, root, phase := "unknown", "—", "unreadable"
		if state := description.State; state != nil {
			mode = "read/write"
			if state.ReadOnly {
				mode = "read-only"
			}
			root, phase = state.Public, state.Phase
		}
		next := "pvedisk recover " + id
		if canUnmount(description) {
			next = "pvedisk unmount " + id
		}
		if description.VMID == 0 {
			id = "invalid record"
			next = "inspect your repair state"
		}
		observed := description.Observation.Status
		if observed == "" {
			observed = "unknown"
		}
		if description.Error != "" {
			observed = "unreadable"
		}
		rows = append(rows, []string{id, observed, phase, mode, root, next})
		if description.Error != "" {
			rows = append(rows, []string{"", "error: " + description.Error, "", "", "", ""})
		} else if description.Observation.Error != "" {
			rows = append(rows, []string{"", "note: " + description.Observation.Error, "", "", "", ""})
		}
		if description.State != nil && description.State.Error != "" {
			rows = append(rows, []string{"", "recorded error: " + description.State.Error, "", "", "", ""})
		}
	}
	if err := printTable(output, "Your repair sessions · actual mount observations", []string{"VMID", "ACTUAL MOUNTS", "RECORDED PHASE", "MODE", "GUEST ROOT", "NEXT STEP"}, rows); err != nil {
		return err
	}
	_, err := fmt.Fprintln(output, outputPalette(output).muted.Render("Only pvedisk sessions are listed. Complete cleanup or recovery of incomplete or uncertain sessions before starting your guests."))
	return err
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
	p := outputPalette(output)
	fmt.Fprintln(output, p.title.Render(title))
	var table bytes.Buffer
	w := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, row := range rows {
		clean := make([]string, len(row))
		for index, cell := range row {
			clean[index] = strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return ' '
				}
				return r
			}, cell)
		}
		fmt.Fprintln(w, strings.Join(clean, "\t"))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	header, body, _ := strings.Cut(table.String(), "\n")
	_, err := fmt.Fprintln(output, p.label.Render(header))
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(output, body)
	return err
}

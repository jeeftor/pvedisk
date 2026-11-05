package cli

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/spf13/cobra"
)

type workerEntry struct {
	VMID        int    `json:"vmid"`
	PID         int    `json:"pid"`
	Mode        string `json:"mode"`
	MountStatus string `json:"mount_status"`
	Path        string `json:"path"`
}

func psCommand(ops operations) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use: "ps", Short: "List your running pvedisk mount workers",
		Long: "List your running pvedisk mount workers after checking their saved process identities. Startup and failed workers are included while they are alive; MOUNTS shows their current mount status. Saved sessions without a running worker are counted separately because their disks may still be mounted. Use status to inspect all saved sessions.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessions, err := ops.listSessions()
			if err != nil {
				return fmt.Errorf("list your pvedisk workers: %w", err)
			}
			entries, inactive, uncertain := runningWorkers(sessions)
			if asJSON {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(entries); err != nil {
					return err
				}
			} else if len(entries) == 0 {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "No running pvedisk workers."); err != nil {
					return err
				}
			} else {
				var rows [][]string
				for _, entry := range entries {
					status := entry.MountStatus
					switch status {
					case "not-mounted":
						status = "not mounted"
					case "partial":
						status = "partly mounted"
					}
					rows = append(rows, []string{strconv.Itoa(entry.VMID), strconv.Itoa(entry.PID), entry.Mode, status, entry.Path})
				}
				if err := printTable(cmd.OutOrStdout(), "Your running pvedisk workers", []string{"VMID", "PID", "MODE", "MOUNTS", "PATH"}, rows); err != nil {
					return err
				}
			}
			if inactive > 0 || uncertain > 0 {
				output := cmd.OutOrStdout()
				if asJSON {
					output = cmd.ErrOrStderr()
				}
				p := outputPalette(output)
				if inactive > 0 {
					if _, err := fmt.Fprintln(output, p.warning.Render(fmt.Sprintf("Saved sessions without a running worker: %d.", inactive))); err != nil {
						return err
					}
				}
				if uncertain > 0 {
					if _, err := fmt.Fprintln(output, p.warning.Render(fmt.Sprintf("Saved sessions whose worker could not be checked: %d.", uncertain))); err != nil {
						return err
					}
				}
				_, err = fmt.Fprintln(output, "Run pvedisk status to inspect these sessions; their disks may still be mounted and need cleanup.")
				return err
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "Print only public worker details as plain JSON")
	return command
}

func runningWorkers(sessions []session.Description) ([]workerEntry, int, int) {
	entries := make([]workerEntry, 0)
	inactive, uncertain := 0, 0
	for _, description := range sessions {
		state := description.State
		if state == nil || description.Error != "" || !description.Observation.WorkerKnown || description.VMID < 100 || description.VMID > 999999999 || state.VMID != description.VMID {
			uncertain++
			continue
		}
		if !description.Observation.WorkerAlive {
			inactive++
			continue
		}
		if state.Worker.PID <= 0 {
			uncertain++
			continue
		}
		mode := "read/write"
		if state.ReadOnly {
			mode = "read-only"
		}
		status := description.Observation.Status
		if status == "" {
			status = "unknown"
		}
		entries = append(entries, workerEntry{VMID: description.VMID, PID: state.Worker.PID, Mode: mode, MountStatus: status, Path: state.Public})
	}
	return entries, inactive, uncertain
}

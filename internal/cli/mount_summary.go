package cli

import (
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"github.com/jeeftor/pvedisk/internal/appliance"
	"github.com/jeeftor/pvedisk/internal/session"
)

func writeMountSummary(output io.Writer, id int, state *session.State) error {
	mode := "read/write"
	p := outputPalette(output)
	modeStyle := p.warning
	if state.ReadOnly {
		mode = "read-only"
		modeStyle = p.success
	}
	if _, err := fmt.Fprintf(output, "%s (%s).\n%s %s\n", p.success.Render(fmt.Sprintf("Your VM %d is mounted", id)), modeStyle.Render(mode), p.label.Render("Guest root:"), p.path.Render(state.Public)); err != nil {
		return err
	}
	for _, fs := range state.Inventory.Filesystems {
		if fs.Mounted && fs.Extra {
			if _, err := fmt.Fprintf(output, "%s %s\n", p.label.Render("Additional filesystems:"), p.path.Render(state.Extras)); err != nil {
				return err
			}
			break
		}
	}
	if len(state.Inventory.Filesystems) > 0 {
		var rows [][]string
		for _, fs := range state.Inventory.Filesystems {
			point := fs.Mountpoint
			if !fs.Mounted {
				point = "not mounted"
			} else if fs.Extra {
				point = "extra"
			}
			rows = append(rows, []string{fs.Device, fs.Type, point, filesystemHostPath(state, fs)})
		}
		fmt.Fprintln(output)
		if err := printTable(output, "Your filesystems", []string{"DEVICE", "TYPE", "GUEST MOUNTPOINT", "HOST PATH"}, rows); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(output, "%s %s\n", p.muted.Render("When you finish, run:"), p.path.Render(fmt.Sprintf("pvedisk unmount %d", id)))
	return err
}

func filesystemHostPath(state *session.State, fs appliance.Filesystem) string {
	if !fs.Mounted {
		return "—"
	}
	if fs.Extra {
		if !filepath.IsAbs(state.Extras) || fs.ID == "" || fs.ID == "." || fs.ID == ".." || filepath.Base(fs.ID) != fs.ID {
			return "—"
		}
		return filepath.Join(state.Extras, fs.ID)
	}
	if !filepath.IsAbs(state.Public) || !path.IsAbs(fs.Mountpoint) || path.Clean(fs.Mountpoint) != fs.Mountpoint {
		return "—"
	}
	return filepath.Join(state.Public, strings.TrimPrefix(fs.Mountpoint, "/"))
}

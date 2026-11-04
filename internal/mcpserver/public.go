package mcpserver

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jeeftor/pvedisk/internal/session"
)

// Filesystem describes a filesystem and its public host location.
type Filesystem struct {
	Device                string `json:"device"`
	Type                  string `json:"type"`
	GuestMountpoint       string `json:"guest_mountpoint"`
	HostPath              string `json:"host_path"`
	Mounted               bool   `json:"mounted"`
	GuestMountedAtPrepare bool   `json:"guest_mounted_at_prepare"`
	Extra                 bool   `json:"extra"`
}

// PublicSession exposes repair status without private ownership or control data.
type PublicSession struct {
	VMID        int                 `json:"vmid"`
	Phase       string              `json:"phase"`
	Error       string              `json:"error,omitempty"`
	Mode        string              `json:"mode"`
	WorkerPID   int                 `json:"worker_pid"`
	Root        string              `json:"root"`
	Extras      string              `json:"extras"`
	Observation session.Observation `json:"observation"`
	Filesystems []Filesystem        `json:"filesystems"`
}

var privatePath = regexp.MustCompile(`/var/lib/pvedisk/[^\s:;,"')]+`)
var ownershipToken = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

func publicError(text string, state *session.State) string {
	if state != nil {
		for _, secret := range []string{state.Token, state.PasswordFile} {
			if secret != "" {
				text = strings.ReplaceAll(text, secret, "[private]")
			}
		}
	}
	return ownershipToken.ReplaceAllString(privatePath.ReplaceAllString(text, "[private]"), "[private]")
}

func publicSession(description session.Description) PublicSession {
	s := description.State
	result := PublicSession{VMID: description.VMID, Phase: "unknown", Mode: "unknown", Observation: description.Observation, Filesystems: make([]Filesystem, 0)}
	result.Error = publicError(description.Error, s)
	result.Observation.Error = publicError(result.Observation.Error, s)
	result.Observation.Status = publicError(result.Observation.Status, s)
	if s == nil {
		return result
	}
	result.Phase = publicError(s.Phase, s)
	result.Mode = "read/write"
	if s.ReadOnly {
		result.Mode = "read-only"
	}
	result.WorkerPID = s.Worker.PID
	result.Root = publicError(s.Public, s)
	result.Extras = publicError(s.Extras, s)
	if s.Error != "" {
		if result.Error != "" {
			result.Error += "; "
		}
		result.Error += publicError(s.Error, s)
	}
	for _, fs := range s.Inventory.Filesystems {
		item := Filesystem{Device: fs.Device, Type: fs.Type, GuestMountpoint: fs.Mountpoint, GuestMountedAtPrepare: fs.Mounted, Extra: fs.Extra}
		observed := description.Observation
		if fs.Mounted && observed.MountsKnown && observed.WorkerKnown && observed.WorkerAlive && observed.Status != "conflict" {
			item.Mounted = observed.RootMounted && !fs.Extra || observed.ExtrasMounted && fs.Extra
		}
		if fs.Mounted {
			if fs.Extra {
				item.GuestMountpoint = ""
				if filepath.IsAbs(s.Extras) && fs.ID != "" && fs.ID != "." && fs.ID != ".." && filepath.Base(fs.ID) == fs.ID {
					item.HostPath = filepath.Join(s.Extras, fs.ID)
				}
			} else if filepath.IsAbs(s.Public) && path.IsAbs(fs.Mountpoint) && path.Clean(fs.Mountpoint) == fs.Mountpoint {
				item.HostPath = filepath.Join(s.Public, strings.TrimPrefix(fs.Mountpoint, "/"))
			}
		}
		item.Device = publicError(item.Device, s)
		item.Type = publicError(item.Type, s)
		item.GuestMountpoint = publicError(item.GuestMountpoint, s)
		item.HostPath = publicError(item.HostPath, s)
		result.Filesystems = append(result.Filesystems, item)
	}
	return result
}

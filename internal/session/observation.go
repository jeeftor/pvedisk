package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Observation describes current host mounts and process identity separately from
// the lifecycle phase recorded before a crash or failed cleanup.
type Observation struct {
	RootMounted    bool   `json:"root_mounted"`
	ExtrasMounted  bool   `json:"extras_mounted"`
	StagingMounted bool   `json:"staging_mounted"`
	MountsKnown    bool   `json:"mounts_known"`
	WorkerAlive    bool   `json:"worker_alive"`
	WorkerKnown    bool   `json:"worker_known"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
}

// Description combines one persistent record with a fresh, read-only observation.
// A malformed record has an Error and no State, so a listing never hides it.
type Description struct {
	VMID        int         `json:"vmid"`
	Record      string      `json:"record"`
	State       *State      `json:"state,omitempty"`
	Observation Observation `json:"observation"`
	Error       string      `json:"error,omitempty"`
}

// Describe observes a recorded session without loading configuration or changing
// VM state, mount paths, or persistent records.
func (m *Manager) Describe(id int) (*Description, error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if err := m.inspectRoot(); err != nil {
		return nil, err
	}
	s, err := m.readState(id)
	if err != nil {
		return nil, err
	}
	return &Description{VMID: id, Record: filepath.Base(m.statePath(id)), State: s, Observation: observeState(s)}, nil
}

// List returns every persistent session record in VM order, including corrupt
// records as error rows. An absent state directory is an empty list, not setup.
func (m *Manager) List() ([]Description, error) {
	rows := make([]Description, 0)
	if err := m.inspectRoot(); errors.Is(err, os.ErrNotExist) {
		return rows, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		row := Description{Record: entry.Name(), Observation: Observation{Status: "unknown"}}
		id, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			row.Error = "invalid session record filename"
			rows = append(rows, row)
			continue
		}
		row.VMID = id
		if err := validID(id); err != nil {
			row.Error = err.Error()
			rows = append(rows, row)
			continue
		}
		if entry.Name() != strconv.Itoa(id)+".json" {
			row.Error = "noncanonical session record filename"
			rows = append(rows, row)
			continue
		}
		s, err := m.readState(id)
		if err != nil {
			row.Error = err.Error()
			rows = append(rows, row)
			continue
		}
		row.State = s
		row.Observation = observeState(s)
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].VMID == rows[j].VMID {
			return rows[i].Record < rows[j].Record
		}
		if rows[i].VMID == 0 {
			return false
		}
		if rows[j].VMID == 0 {
			return true
		}
		return rows[i].VMID < rows[j].VMID
	})
	return rows, nil
}

func observeState(s *State) Observation {
	if runtime.GOOS != "linux" {
		return Observation{Status: "unknown", Error: "mount and worker observations require Linux on your Proxmox host"}
	}
	data, mountErr := os.ReadFile("/proc/self/mountinfo")
	workerAlive, workerErr := alive(s.Worker)
	return observe(s, string(data), mountErr, workerAlive, workerErr)
}

func observe(s *State, data string, mountErr error, workerAlive bool, workerErr error) Observation {
	o := Observation{Status: "unknown", WorkerAlive: workerAlive, WorkerKnown: workerErr == nil}
	if workerErr != nil {
		o.Error = "inspect recorded worker: " + workerErr.Error()
	}
	if mountErr != nil {
		o.Error = joinObservationErrors(o.Error, "inspect actual mounts: "+mountErr.Error())
		return o
	}
	mounts, err := parseObservedMounts(data)
	if err != nil {
		o.Error = joinObservationErrors(o.Error, err.Error())
		return o
	}
	o.MountsKnown = true
	var conflicts []string
	for _, mount := range mounts {
		for _, path := range []string{s.Public, s.Extras, s.Staging} {
			if path == "" {
				continue
			}
			if mount.target == path {
				switch path {
				case s.Public:
					o.RootMounted = true
				case s.Extras:
					o.ExtrasMounted = true
				case s.Staging:
					o.StagingMounted = true
				}
				if !strings.HasPrefix(mount.kind, "fuse") || mount.source != "pvedisk-"+s.Token {
					conflicts = append(conflicts, fmt.Sprintf("mount at %s belongs to another filesystem or session", path))
				}
			} else if strings.HasPrefix(mount.target, path+"/") {
				conflicts = append(conflicts, fmt.Sprintf("unexpected nested mount at %s", mount.target))
			}
		}
	}
	if len(conflicts) != 0 {
		o.Status = "conflict"
		o.Error = joinObservationErrors(o.Error, strings.Join(conflicts, "; "))
		return o
	}
	if workerErr != nil {
		return o
	}
	if !o.RootMounted && !o.ExtrasMounted && !o.StagingMounted {
		o.Status = "not-mounted"
		return o
	}
	if o.RootMounted && o.ExtrasMounted && o.StagingMounted && o.WorkerAlive {
		o.Status = "mounted"
		return o
	}
	o.Status = "partial"
	if !o.WorkerAlive {
		o.Error = "your recorded worker is not running; use recover"
	} else {
		o.Error = "one or more recorded mount views are missing"
	}
	return o
}

type observedMount struct{ target, kind, source string }

func parseObservedMounts(data string) ([]observedMount, error) {
	var mounts []observedMount
	for _, line := range strings.Split(data, "\n") {
		if line == "" {
			continue
		}
		sep := strings.Index(line, " - ")
		if sep < 0 {
			return nil, errors.New("invalid mount information")
		}
		fields := strings.Fields(line[:sep])
		tail := strings.Fields(line[sep+3:])
		if len(fields) < 6 || len(tail) < 3 {
			return nil, errors.New("invalid mount information")
		}
		mounts = append(mounts, observedMount{target: decodeMount(fields[4]), kind: tail[0], source: decodeMount(tail[1])})
	}
	return mounts, nil
}

func joinObservationErrors(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

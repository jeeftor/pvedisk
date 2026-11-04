// Package buildinfo exposes metadata stamped into pvedisk by its build process.
package buildinfo

import "runtime"

var (
	// Version is a release tag or development version supplied by scripts/build.sh.
	Version = "dev"
	// Commit is the full source revision used for this executable.
	Commit = "unknown"
	// BuildDate is the UTC build timestamp in RFC3339 format.
	BuildDate = "unknown"
	// Source identifies the build environment: github, gitlab, or local.
	Source = "local"
)

// Info combines stamped source metadata with the executable's Go runtime target.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Source    string `json:"source"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Current returns the metadata embedded in the running executable.
func Current() Info {
	return Info{
		Version: Version, Commit: Commit, BuildDate: BuildDate, Source: Source,
		GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
	}
}

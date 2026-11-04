package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeeftor/pvedisk/internal/buildinfo"
	"github.com/jeeftor/pvedisk/internal/config"
)

func TestVersionMetadataAndConfigurationIndependence(t *testing.T) {
	oldVersion, oldCommit, oldDate, oldSource := buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate, buildinfo.Source
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate, buildinfo.Source = oldVersion, oldCommit, oldDate, oldSource
	})
	buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate, buildinfo.Source = "v9.8.7", "abc123", "2026-11-04T12:00:00Z", "github"
	ops := testOperations()
	ops.load = func(string) (config.Config, error) {
		t.Fatal("version read configuration")
		return config.Config{}, nil
	}
	for _, args := range [][]string{{"version", "--short"}, {"--version"}} {
		output, err := execute(ops, args...)
		if err != nil || output != "v9.8.7\n" {
			t.Fatalf("args=%v output=%q err=%v", args, output, err)
		}
	}
	output, err := execute(ops, "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var info buildinfo.Info
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version != "v9.8.7" || info.Commit != "abc123" || info.BuildDate != "2026-11-04T12:00:00Z" || info.Source != "github" || info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Fatalf("incomplete metadata: %+v", info)
	}
	output, err = execute(ops, "version")
	for _, want := range []string{"pvedisk", "v9.8.7", "abc123", "2026-11-04T12:00:00Z", "github", info.GoVersion, info.OS + "/" + info.Arch} {
		if err != nil || !strings.Contains(output, want) {
			t.Fatalf("missing %q: output=%q err=%v", want, output, err)
		}
	}
	if strings.Contains(output, "\x1b") {
		t.Fatal("redirected version has ANSI escapes")
	}
}

func TestVersionOutputFlagsAreMutuallyExclusive(t *testing.T) {
	if _, err := execute(testOperations(), "version", "--short", "--json"); err == nil {
		t.Fatal("accepted incompatible version output modes")
	}
}

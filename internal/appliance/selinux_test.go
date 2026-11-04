package appliance

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func selinuxFixture() (map[string]guestFileMetadata, func(string, int, int64) ([]byte, error)) {
	files := map[string]guestFileMetadata{
		"/root":                    {mode: 0040755},
		"/root/etc":                {mode: 0040755},
		"/root/etc/selinux":        {mode: 0040755},
		"/root/etc/selinux/config": {mode: 0100644, size: 36},
	}
	return files, func(string, int, int64) ([]byte, error) {
		return []byte("SELINUX=enforcing\nSELINUXTYPE=targeted\n"), nil
	}
}

func fixtureGuestStat(files map[string]guestFileMetadata) func(string) (guestFileMetadata, error) {
	return func(path string) (guestFileMetadata, error) {
		info, ok := files[path]
		if !ok {
			return guestFileMetadata{}, fs.ErrNotExist
		}
		return info, nil
	}
}

func TestSELinuxConfigProbeReadsBoundedData(t *testing.T) {
	files, _ := selinuxFixture()
	files["/root/.autorelabel"] = guestFileMetadata{mode: 0100600}
	called := false
	data, err := readSELinuxConfig(fixtureGuestStat(files), func(path string, count int, offset int64) ([]byte, error) {
		called = true
		if path != "/root/etc/selinux/config" || count != maxSELinuxConfigSize+1 || offset != 0 {
			t.Fatal("probe did not constrain its read")
		}
		return []byte("SELINUX=permissive\n"), nil
	})
	if err != nil || !called || string(data) != "SELINUX=permissive\n" {
		t.Fatalf("probe data=%q error=%v", data, err)
	}
}

func TestSELinuxConfigProbeRejectsSymlinksAndSpecialFiles(t *testing.T) {
	for _, path := range []string{"/root", "/root/etc", "/root/etc/selinux", "/root/etc/selinux/config", "/root/.autorelabel"} {
		for _, mode := range []int64{0120777, 0010600, 0060600} {
			t.Run(path, func(t *testing.T) {
				files, _ := selinuxFixture()
				files[path] = guestFileMetadata{mode: mode}
				if _, err := readSELinuxConfig(fixtureGuestStat(files), func(string, int, int64) ([]byte, error) { t.Fatal("unsafe path was read"); return nil, nil }); err == nil {
					t.Fatal("unsafe guest path accepted")
				}
			})
		}
	}
}

func TestSELinuxConfigProbeMissingAndOversized(t *testing.T) {
	files, read := selinuxFixture()
	delete(files, "/root/etc/selinux/config")
	if _, err := readSELinuxConfig(fixtureGuestStat(files), read); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing guest configuration error=%v", err)
	}
	files, read = selinuxFixture()
	files["/root/etc/selinux/config"] = guestFileMetadata{mode: 0100644, size: maxSELinuxConfigSize + 1}
	if _, err := readSELinuxConfig(fixtureGuestStat(files), read); err == nil {
		t.Fatal("oversized file metadata accepted")
	}
	files, _ = selinuxFixture()
	buffer := []byte(strings.Repeat("x", maxSELinuxConfigSize+1))
	if _, err := readSELinuxConfig(fixtureGuestStat(files), func(string, int, int64) ([]byte, error) { return buffer, nil }); err == nil {
		t.Fatal("oversized read accepted")
	}
	for _, b := range buffer {
		if b != 0 {
			t.Fatal("rejected configuration data was not cleared")
		}
	}
}

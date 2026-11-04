//go:build linux && cgo

package password

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestYescryptRoundTrip(t *testing.T) {
	password := []byte("native secret with punctuation: $ !")
	defer clear(password)
	before := bytes.Clone(password)
	defer clear(before)
	first, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "$y$") || !strings.HasPrefix(second, "$y$") {
		t.Fatal("generation did not select yescrypt")
	}
	if first == second {
		t.Fatal("generation reused a salt")
	}
	for _, encoded := range []string{first, second} {
		if ok, err := Verify(password, encoded); err != nil || !ok {
			t.Fatalf("correct candidate rejected: %v", err)
		}
		if ok, err := Verify([]byte("incorrect candidate"), encoded); err != nil || ok {
			t.Fatalf("incorrect candidate result = %v, %v", ok, err)
		}
	}
	if !bytes.Equal(password, before) {
		t.Fatal("caller password buffer changed")
	}
}

func TestInvalidAndUnsupportedHash(t *testing.T) {
	for _, tc := range []struct {
		encoded string
		want    error
	}{{"$6$salt$short", ErrInvalidHash}, {"$unsupported$salt$hash", ErrUnsupportedHash}} {
		if _, err := Verify([]byte("candidate"), tc.encoded); !errors.Is(err, tc.want) {
			t.Fatalf("error = %v, want %v", err, tc.want)
		}
	}
	if _, err := Verify([]byte("candidate"), "$6$salt$"+strings.Repeat("?", 86)); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("invalid checksum characters accepted: %v", err)
	}
}

func TestSHA512PublishedVector(t *testing.T) {
	// GNU libc's sha512c-test.c, first published test vector:
	// https://github.com/bminor/glibc/blob/release/2.38/master/crypt/sha512c-test.c
	const encoded = "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"
	if ok, err := Verify([]byte("Hello world!"), encoded); err != nil || !ok {
		t.Fatalf("published SHA-512 vector failed: %v", err)
	}
	if ok, err := Verify([]byte("incorrect candidate"), encoded); err != nil || ok {
		t.Fatalf("incorrect candidate result = %v, %v", ok, err)
	}
}

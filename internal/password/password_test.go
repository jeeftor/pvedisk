package password

import (
	"errors"
	"testing"
)

func TestPasswordValidation(t *testing.T) {
	for _, tc := range []struct {
		value []byte
		want  error
	}{{nil, ErrEmptyPassword}, {[]byte{}, ErrEmptyPassword}, {[]byte("secret\x00ignored"), ErrNULPassword}} {
		if _, err := Hash(tc.value); !errors.Is(err, tc.want) {
			t.Fatalf("Hash error = %v, want %v", err, tc.want)
		}
		if _, err := Verify(tc.value, "$6$salt$hash"); !errors.Is(err, tc.want) {
			t.Fatalf("Verify error = %v, want %v", err, tc.want)
		}
	}
}

func TestHashValidation(t *testing.T) {
	for _, tc := range []struct {
		encoded string
		want    error
	}{{"", ErrEmptyHash}, {"!", ErrLockedHash}, {"!!", ErrLockedHash}, {"!$6$salt$hash", ErrLockedHash}, {"*", ErrLockedHash}, {"*0", ErrLockedHash}, {"$6$salt$", ErrInvalidHash}, {"$6$$", ErrInvalidHash}, {"$6$salt$hash\n", ErrInvalidHash}, {"$6$salt$hash\x00", ErrInvalidHash}, {"garbage", ErrInvalidHash}} {
		if _, err := Verify([]byte("candidate"), tc.encoded); !errors.Is(err, tc.want) {
			t.Fatalf("Verify hash category error = %v, want %v", err, tc.want)
		}
	}
}

// Package password generates and checks Linux password hashes without exposing
// plaintext through subprocess arguments, temporary files, or log messages.
// Functions leave your input buffer unchanged; callers should clear it after use.
package password

import (
	"bytes"
	"errors"
	"strings"
)

var (
	// ErrEmptyPassword rejects an empty candidate password.
	ErrEmptyPassword = errors.New("your password must not be empty")
	// ErrNULPassword rejects passwords that C password interfaces would truncate.
	ErrNULPassword = errors.New("your password must not contain a null byte")
	// ErrEmptyHash identifies an account without a stored password hash.
	ErrEmptyHash = errors.New("your account has an empty password hash")
	// ErrLockedHash identifies a locked account rather than a password mismatch.
	ErrLockedHash = errors.New("your account password is locked")
	// ErrInvalidHash identifies a malformed password hash.
	ErrInvalidHash = errors.New("your stored password hash is invalid")
	// ErrUnsupportedHash identifies an algorithm unavailable in your host library.
	ErrUnsupportedHash = errors.New("your host does not support this password hash algorithm")
	// ErrUnsupportedPlatform identifies builds without native Linux libcrypt.
	ErrUnsupportedPlatform = errors.New("password hashing requires your Linux CGO build with native libcrypt")
	// ErrHashFailed reports a native hashing failure without including secrets.
	ErrHashFailed = errors.New("your host password hashing operation failed")
)

func validatePassword(password []byte) error {
	if len(password) == 0 {
		return ErrEmptyPassword
	}
	if bytes.IndexByte(password, 0) >= 0 {
		return ErrNULPassword
	}
	return nil
}

func validateHash(encoded string) error {
	if encoded == "" {
		return ErrEmptyHash
	}
	if encoded[0] == '!' || encoded[0] == '*' {
		return ErrLockedHash
	}
	for _, c := range encoded {
		if c <= 0x20 || c >= 0x7f || strings.ContainsRune("!:;*\\", c) {
			return ErrInvalidHash
		}
	}
	if encoded[0] == '$' {
		fields := strings.Split(encoded, "$")
		if len(fields) < 4 || fields[1] == "" || fields[len(fields)-1] == "" {
			return ErrInvalidHash
		}
		// These crypt encodings have fixed checksum lengths. Reject a salt
		// or truncated hash before treating it as a password mismatch.
		checksumSize := 0
		switch fields[1] {
		case "1":
			checksumSize = 22
		case "5", "y", "gy", "7", "sm3y", "sm3":
			checksumSize = 43
		case "6":
			checksumSize = 86
		case "3":
			checksumSize = 32
		case "sha1":
			checksumSize = 28
		case "2a", "2b", "2x", "2y":
			checksumSize = 53
		}
		if checksumSize != 0 && len(fields[len(fields)-1]) != checksumSize {
			return ErrInvalidHash
		}
		if checksumSize != 0 {
			for _, c := range fields[len(fields)-1] {
				if !cryptCharacter(c) {
					return ErrInvalidHash
				}
			}
		}
		return nil
	}
	// Traditional DES and extended DES hashes have fixed encoded lengths.
	if !(len(encoded) >= 13 && (len(encoded)-13)%11 == 0) && !(encoded[0] == '_' && len(encoded) == 20) {
		return ErrInvalidHash
	}
	for _, c := range encoded {
		if !cryptCharacter(c) && c != '_' {
			return ErrInvalidHash
		}
	}
	return nil
}

func cryptCharacter(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '/'
}

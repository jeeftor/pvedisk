//go:build !linux || !cgo

package password

// Hash requires your Linux CGO build and native libcrypt.
func Hash(password []byte) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	return "", ErrUnsupportedPlatform
}

// Verify requires your Linux CGO build and native libcrypt.
func Verify(password []byte, encoded string) (bool, error) {
	if err := validatePassword(password); err != nil {
		return false, err
	}
	if err := validateHash(encoded); err != nil {
		return false, err
	}
	return false, ErrUnsupportedPlatform
}

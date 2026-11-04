//go:build linux && cgo

package password

/*
#cgo LDFLAGS: -lcrypt
#define _GNU_SOURCE
#include <crypt.h>
#include <errno.h>
#include <stdlib.h>
#include <string.h>

// A volatile erase prevents the compiler from optimizing away secret cleanup.
static void pvedisk_erase(void *p, size_t n) {
  volatile unsigned char *q = (volatile unsigned char *)p;
  while (n--) *q++ = 0;
}
static void pvedisk_free(void *p, size_t n) {
  if (p) { pvedisk_erase(p, n); free(p); }
}
static char *pvedisk_crypt(const char *password, const char *setting, int *status) {
  struct crypt_data *data = calloc(1, sizeof *data);
  if (!data) { *status = 3; return NULL; }
  errno = 0;
  char *value = crypt_r(password, setting, data);
  char *result = NULL;
  if (!value || value[0] == '*') {
    *status = (errno == ENOSYS || errno == EOPNOTSUPP || errno == EPERM) ? 2 :
      (errno == EINVAL ? 1 : 3);
  } else {
    result = strdup(value);
    if (!result) *status = 3;
  }
  pvedisk_free(data, sizeof *data);
  return result;
}
static char *pvedisk_hash(const char *password, const char *random, int count, int *status) {
  char salt[CRYPT_GENSALT_OUTPUT_SIZE];
  char *result = NULL;
  errno = 0;
  if (!crypt_gensalt_rn("$y$", 0, random, count, salt, sizeof salt)) {
    *status = (errno == EINVAL || errno == ENOSYS || errno == EOPNOTSUPP) ? 2 : 3;
  } else {
    result = pvedisk_crypt(password, salt, status);
  }
  pvedisk_erase(salt, sizeof salt);
  return result;
}
static int pvedisk_checksalt(const char *setting) {
  int result = crypt_checksalt(setting);
  if (result == CRYPT_SALT_METHOD_DISABLED) return 2;
  if (result == CRYPT_SALT_INVALID) return 1;
  return 0;
}
*/
import "C"

import (
	"crypto/rand"
	"crypto/subtle"
	"unsafe"
)

// Hash creates a yescrypt password hash with a fresh cryptographically random
// salt and your host libcrypt's default cost. It does not clear your input buffer.
func Hash(password []byte) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", ErrHashFailed
	}
	defer clear(random[:])
	secret := cPassword(password)
	defer C.pvedisk_free(unsafe.Pointer(secret), C.size_t(len(password)+1))
	entropy := C.CBytes(random[:])
	defer C.pvedisk_free(entropy, C.size_t(len(random)))
	var status C.int
	value := C.pvedisk_hash(secret, (*C.char)(entropy), C.int(len(random)), &status)
	return result(value, status)
}

// Verify compares a candidate against your encoded Linux shadow password using
// your host's crypt algorithms. A mismatch returns false,nil; locked, empty,
// malformed and unsupported hashes return distinct errors. Comparisons are
// constant time. It does not clear your input buffer.
func Verify(password []byte, encoded string) (bool, error) {
	if err := validatePassword(password); err != nil {
		return false, err
	}
	if err := validateHash(encoded); err != nil {
		return false, err
	}
	setting := C.CString(encoded)
	defer C.pvedisk_free(unsafe.Pointer(setting), C.size_t(len(encoded)+1))
	if code := C.pvedisk_checksalt(setting); code != 0 {
		// Generic encoding characters were already checked. libxcrypt's
		// checksalt classifies an unknown algorithm prefix as INVALID.
		return false, ErrUnsupportedHash
	}
	secret := cPassword(password)
	defer C.pvedisk_free(unsafe.Pointer(secret), C.size_t(len(password)+1))
	var status C.int
	value := C.pvedisk_crypt(secret, setting, &status)
	actual, err := result(value, status)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(encoded)) == 1, nil
}

func cPassword(password []byte) *C.char {
	p := C.malloc(C.size_t(len(password) + 1))
	buffer := unsafe.Slice((*byte)(p), len(password)+1)
	copy(buffer, password)
	buffer[len(password)] = 0
	return (*C.char)(p)
}

func result(value *C.char, status C.int) (string, error) {
	if value != nil {
		defer C.pvedisk_free(unsafe.Pointer(value), C.strlen(value)+1)
	}
	if status == 2 {
		return "", ErrUnsupportedHash
	}
	if status == 1 {
		return "", ErrInvalidHash
	}
	if status != 0 || value == nil {
		return "", ErrHashFailed
	}
	return C.GoString(value), nil
}

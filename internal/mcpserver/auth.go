package mcpserver

import (
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxTokenSize = 4096

// ReadToken checks the opened file rather than its pathname so replacing the
// pathname cannot substitute an unchecked file between inspection and reading.
func ReadToken(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("your bearer token file path must be absolute")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("could not securely open your bearer token file")
	}
	file := os.NewFile(uintptr(fd), "bearer token")
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, errors.New("could not inspect your bearer token file")
	}
	mode := stat.Mode & 07777
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != os.Geteuid() || (mode != 0600 && mode != 0400) {
		return nil, errors.New("your bearer token file must be a regular file owned by your effective user with permissions 0600 or 0400")
	}
	buffer := make([]byte, maxTokenSize+1)
	n, err := io.ReadFull(file, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		clear(buffer)
		return nil, errors.New("could not read your bearer token file")
	}
	if n > maxTokenSize {
		clear(buffer)
		return nil, errors.New("your bearer token file exceeds the 4096-byte limit")
	}
	end := n
	if end > 0 && buffer[end-1] == '\n' {
		end--
		if end > 0 && buffer[end-1] == '\r' {
			end--
		}
	}
	valid := end >= 32
	for _, b := range buffer[:end] {
		if b < 33 || b > 126 {
			valid = false
		}
	}
	if !valid {
		clear(buffer)
		return nil, errors.New("your bearer token must contain at least 32 printable ASCII characters without whitespace")
	}
	clear(buffer[end:])
	return buffer[:end], nil
}

// authenticate requires one Authorization field on every request. Cookies and
// query parameters never supply credentials, and rejected values are not echoed.
func authenticate(next http.Handler, token []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var authorization string
		count := 0
		for key, values := range r.Header {
			if strings.EqualFold(key, "Authorization") {
				count += len(values)
				if len(values) == 1 {
					authorization = values[0]
				}
			}
		}
		scheme, supplied, ok := strings.Cut(authorization, " ")
		if count != 1 || !ok || !strings.EqualFold(scheme, "Bearer") || len(token) == 0 || subtle.ConstantTimeCompare([]byte(supplied), token) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="pvedisk"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

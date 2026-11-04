package mcpserver

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReadToken(t *testing.T) {
	const secret = "sensitive-token-at-least-32-characters-long"
	for _, ending := range []string{"", "\n", "\r\n"} {
		for _, mode := range []os.FileMode{0400, 0600} {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(secret+ending), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			token, err := ReadToken(path)
			if err != nil || string(token) != secret {
				t.Fatalf("valid token rejected or changed: %v", err)
			}
			clear(token)
		}
	}
}

func TestReadTokenRejectsInvalidContents(t *testing.T) {
	const marker = "sensitive-token-marker-do-not-disclose"
	for _, test := range []struct {
		name, contents string
	}{
		{"empty", ""},
		{"short", strings.Repeat("x", 31)},
		{"large", strings.Repeat("x", maxTokenSize+1)},
		{"multiple lines", marker + "\nsecond\n"},
		{"extra newline", marker + "\n\n"},
		{"bare carriage return", marker + "\r"},
		{"space", marker + " "},
		{"tab", marker + "\t"},
		{"zero byte", marker + "\x00"},
		{"control character", marker + "\x1b"},
		{"delete", marker + "\x7f"},
		{"non ASCII", marker + "é"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(test.contents), 0600); err != nil {
				t.Fatal(err)
			}
			token, err := ReadToken(path)
			if err == nil || token != nil {
				t.Fatal("accepted invalid bearer token contents")
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatal("token error disclosed contents")
			}
		})
	}
}

func TestReadTokenRejectsUnsafeFile(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0000, 0200, 0444, 0640, 0644, 0666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if token, err := ReadToken(path); err == nil || token != nil {
			t.Fatalf("accepted unsafe file permissions %04o", mode)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(base, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"relative-token", filepath.Join(base, "missing"), link, base, fifo} {
		if token, err := ReadToken(unsafe); err == nil || token != nil {
			t.Fatalf("accepted unsafe bearer token file %q", unsafe)
		}
	}
}

func TestReadTokenRejectsOtherOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing fixture ownership requires root")
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, -1); err != nil {
		t.Fatal(err)
	}
	if token, err := ReadToken(path); err == nil || token != nil {
		t.Fatal("accepted token file owned by another user")
	}
}

func TestAuthenticateRejectsInvalidCredentials(t *testing.T) {
	const token = "sensitive-token-at-least-32-characters-long"
	for _, test := range []struct {
		name   string
		header http.Header
	}{
		{"missing", nil},
		{"incorrect", http.Header{"Authorization": {"Bearer sensitive-wrong-secret"}}},
		{"first byte mismatch", http.Header{"Authorization": {"Bearer X" + token[1:]}}},
		{"last byte mismatch", http.Header{"Authorization": {"Bearer " + token[:len(token)-1] + "X"}}},
		{"basic", http.Header{"Authorization": {"Basic " + token}}},
		{"missing credential", http.Header{"Authorization": {"Bearer"}}},
		{"empty credential", http.Header{"Authorization": {"Bearer "}}},
		{"tab separator", http.Header{"Authorization": {"Bearer\t" + token}}},
		{"two spaces", http.Header{"Authorization": {"Bearer  " + token}}},
		{"trailing space", http.Header{"Authorization": {"Bearer " + token + " "}}},
		{"duplicate", http.Header{"Authorization": {"Bearer " + token, "Bearer " + token}}},
		{"duplicate case", http.Header{"Authorization": {"Bearer " + token}, "authorization": {"Bearer " + token}}},
		{"joined credentials", http.Header{"Authorization": {"Bearer " + token + ", Bearer " + token}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), []byte(token))
			request := httptest.NewRequest(http.MethodPost, "/mcp?access_token="+token, nil)
			request.Header = test.header.Clone()
			if request.Header == nil {
				request.Header = make(http.Header)
			}
			request.AddCookie(&http.Cookie{Name: "token", Value: token})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if called || response.Code != http.StatusUnauthorized {
				t.Fatal("invalid credentials reached the protected handler")
			}
			if response.Header().Get("WWW-Authenticate") != `Bearer realm="pvedisk"` {
				t.Fatal("missing bearer authentication challenge")
			}
			if bytes.Contains(response.Body.Bytes(), []byte("sensitive")) || strings.Contains(response.Header().Get("WWW-Authenticate"), token) {
				t.Fatal("unauthorized response disclosed credentials")
			}
		})
	}
}

func TestAuthenticateAllowsAllMethodsWithValidBearer(t *testing.T) {
	const token = "sensitive-token-at-least-32-characters-long"
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions, http.MethodHead, "CUSTOM"} {
		for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
			called := false
			handler := authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Method != method {
					t.Fatal("authentication changed the request method")
				}
				w.WriteHeader(http.StatusNoContent)
			}), []byte(token))
			request := httptest.NewRequest(method, "/mcp", nil)
			request.Header.Set("Authorization", scheme+" "+token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if !called || response.Code != http.StatusNoContent {
				t.Fatalf("valid bearer rejected for %s: %d", method, response.Code)
			}
		}
	}
}

func TestAuthenticateRejectsEmptyConfiguredToken(t *testing.T) {
	handler := authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("empty token authorized a request") }), nil)
	request := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer ")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("empty configured token did not fail closed")
	}
}

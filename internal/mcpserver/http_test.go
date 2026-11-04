package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tokenTransport struct {
	token string
}

func (transport tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+transport.token)
	return http.DefaultTransport.RoundTrip(copy)
}

func TestHTTPToolRoundTripWithOptionalAuthentication(t *testing.T) {
	for _, token := range []string{"", strings.Repeat("a", 32)} {
		t.Run(map[bool]string{true: "token", false: "no-token"}[token != ""], func(t *testing.T) {
			server := httptest.NewServer(Handler(newServer("/missing-site-config", fixtureOps(t)), []byte(token)))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "1"}, nil)
			transport := &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1}
			if token != "" {
				transport.HTTPClient = &http.Client{Transport: tokenTransport{token: token}}
			}
			session, err := client.Connect(ctx, transport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := session.ListTools(ctx, nil)
			if err != nil || len(tools.Tools) != 9 {
				t.Fatalf("tools=%+v err=%v", tools, err)
			}
			for _, name := range []string{"version", "list_vms", "list_sessions", "ps", "doctor"} {
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
				if err != nil || result.IsError || result.StructuredContent == nil {
					t.Fatalf("%s result=%+v err=%v", name, result, err)
				}
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{"vmid": 400}})
			if err != nil || result.IsError || strings.Contains(resultJSON(t, result), "private-process-start") {
				t.Fatalf("status=%+v err=%v", result, err)
			}
		})
	}
}

func TestHTTPRejectsUnauthorizedAndCrossOriginRequests(t *testing.T) {
	for _, tc := range []struct {
		name, token, header, origin string
		status                      int
	}{
		{"missing-token", "secret", "", "", http.StatusUnauthorized},
		{"wrong-token", "secret", "Bearer wrong", "", http.StatusUnauthorized},
		{"foreign-origin", "", "", "http://other.example", http.StatusForbidden},
		{"authenticated-foreign-origin", "secret", "Bearer secret", "http://other.example", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", strings.NewReader(`{}`))
			if tc.header != "" {
				request.Header.Set("Authorization", tc.header)
			}
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			response := httptest.NewRecorder()
			Handler(newServer("/missing", fixtureOps(t)), []byte(tc.token)).ServeHTTP(response, request)
			if response.Code != tc.status || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestHTTPOriginsMatchHostSchemeAndPort(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"", true}, {"http://pve.example:8766", true}, {"http://PVE.EXAMPLE:8766", true},
		{"http://pve.example", false}, {"https://pve.example:8766", false}, {"http://pve.example:8767", false},
		{"null", false}, {"http://user@pve.example:8766", false}, {"http://pve.example:8766/", false},
		{"http://pve.example:8766?x", false}, {"http://elsewhere:8766", false},
	} {
		request := httptest.NewRequest(http.MethodPost, "http://pve.example:8766/mcp", nil)
		if tc.origin != "" {
			request.Header.Set("Origin", tc.origin)
		}
		if got := allowedOrigin(request); got != tc.want {
			t.Fatalf("origin=%q got=%v want=%v", tc.origin, got, tc.want)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://pve.example/mcp", nil)
	request.Header.Set("Origin", "http://pve.example:80")
	if !allowedOrigin(request) {
		t.Fatal("rejected equivalent default port")
	}
	request.Header.Add("Origin", "http://pve.example")
	if allowedOrigin(request) {
		t.Fatal("accepted ambiguous multiple origins")
	}
}

func TestHTTPBoundAddressControlsOptionalTokenPolicy(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1", "0.0.0.0", "::", "192.0.2.10"} {
		addr := &net.TCPAddr{IP: net.ParseIP(address), Port: 8766}
		loopback := addr.IP.IsLoopback()
		if err := validateListener(addr, false, false); (err == nil) != loopback {
			t.Fatalf("address=%s err=%v", address, err)
		}
		for _, auth := range []bool{false, true} {
			if err := validateListener(addr, auth, true); err != nil {
				t.Fatal(err)
			}
		}
		if err := validateListener(addr, true, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHTTPRejectsOversizedRequests(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", strings.NewReader(strings.Repeat(" ", (1<<20)+1)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	Handler(New("/missing"), nil).ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestRunHTTPRejectsInvalidOptionsBeforeServing(t *testing.T) {
	for _, options := range []HTTPOptions{
		{Listen: "127.0.0.1:0", TokenFile: "/missing", NoToken: true},
		{Listen: "127.0.0.1:0", TokenFile: "/missing"},
		{Listen: "0.0.0.0:0"},
	} {
		var output bytes.Buffer
		if err := RunHTTP(context.Background(), "/missing", options, &output); err == nil || output.Len() != 0 {
			t.Fatalf("options=%+v output=%q err=%v", options, output.String(), err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunHTTP(ctx, "/missing", HTTPOptions{}, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled server: %v", err)
	}
}

func TestHTTPShutdownCancelsActiveCallsAndReleasesListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, canceled := make(chan struct{}), make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	})
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, listener, handler) }()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get("http://" + address + "/mcp")
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP shutdown did not finish")
	}
	<-canceled
	<-requestDone
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		connection.Close()
		t.Fatal("listener remains open after shutdown")
	}
}

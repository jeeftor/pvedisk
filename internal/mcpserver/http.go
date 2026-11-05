package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPOptions selects the listener and optional shared-secret authentication.
type HTTPOptions struct {
	Listen    string
	TokenFile string
	NoToken   bool
}

// RunHTTP serves MCP until cancellation. Stopping HTTP leaves repair workers
// and their VM protection intact; callers must explicitly unmount or clean.
func RunHTTP(ctx context.Context, configPath string, options HTTPOptions, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.TokenFile != "" && options.NoToken {
		return errors.New("use either --token-file or --no-token, not both")
	}
	var token []byte
	if options.TokenFile != "" {
		var err error
		token, err = ReadToken(options.TokenFile)
		if err != nil {
			return err
		}
	}
	if options.Listen == "" {
		options.Listen = "127.0.0.1:8766"
	}
	listener, err := net.Listen("tcp", options.Listen)
	if err != nil {
		return fmt.Errorf("listen for MCP: %w", err)
	}
	defer listener.Close()
	if err := validateListener(listener.Addr(), len(token) > 0, options.NoToken); err != nil {
		return err
	}
	auth := "disabled (loopback)"
	if len(token) > 0 {
		auth = "bearer token"
	} else if options.NoToken {
		auth = "disabled (--no-token)"
	}
	if _, err := fmt.Fprintf(output, "Serving pvedisk MCP at http://%s/mcp\nAuthentication: %s\n", listener.Addr(), auth); err != nil {
		return err
	}
	return serveHTTP(ctx, listener, Handler(New(configPath), token))
}

func validateListener(address net.Addr, authenticated, noToken bool) error {
	tcp, ok := address.(*net.TCPAddr)
	if !ok || tcp.IP == nil {
		return errors.New("cannot identify your MCP listen address")
	}
	if !tcp.IP.IsLoopback() && !authenticated && !noToken {
		return errors.New("for your LAN listener, provide --token-file or explicitly choose --no-token")
	}
	return nil
}

// Handler serves Streamable HTTP at /mcp. Authentication is optional; browser
// origins and the SDK's localhost DNS-rebinding checks remain enforced.
func Handler(server *mcp.Server, token []byte) http.Handler {
	protocol := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, PropagateRequestCancellation: true, MaxRequestBodyBytes: 1 << 20,
	})
	var endpoint http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedOrigin(r) {
			http.Error(w, "your request origin is not allowed", http.StatusForbidden)
			return
		}
		protocol.ServeHTTP(w, r)
	})
	if len(token) > 0 {
		endpoint = authenticate(endpoint, token)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", endpoint)
	return mux
}

func allowedOrigin(request *http.Request) bool {
	origins := request.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.User != nil || origin.Hostname() == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Opaque != "" {
		return false
	}
	host, err := url.Parse("//" + request.Host)
	if err != nil || !strings.EqualFold(origin.Hostname(), host.Hostname()) {
		return false
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return origin.Scheme == scheme && effectivePort(origin, scheme) == effectivePort(host, scheme)
}

func effectivePort(address *url.URL, scheme string) string {
	if port := address.Port(); port != "" {
		return port
	}
	if scheme == "https" {
		return "443"
	}
	return "80"
}

func serveHTTP(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second,
		MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx },
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve MCP: %w", err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
		<-done
		return nil
	}
}

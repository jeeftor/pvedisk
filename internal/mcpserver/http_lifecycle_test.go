package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeeftor/pvedisk/internal/session"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func lifecycleHTTPClient(t *testing.T, endpoint string, options *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "http-lifecycle-test", Version: "1"}, options)
	connection, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	return connection
}

func lifecycleHTTPCall(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s returned a transport error: %v", name, err)
	}
	return result
}

func lifecycleHTTPAwait(t *testing.T, event <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertHTTPLifecycleRedacted(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{strings.Repeat("a", 64), "/var/lib/pvedisk", "/secret/password-file", "private-process-start", "private-boot"} {
		if strings.Contains(text, secret) {
			t.Fatalf("HTTP output disclosed private lifecycle information: %s", text)
		}
	}
}

func TestHTTPMountProgressAndReadOnlyMode(t *testing.T) {
	ops := fixtureOps(t)
	var mounted atomic.Bool
	ops.mount = func(_ context.Context, id int, configPath string, readOnly bool, progress func(string)) error {
		if id != 400 || configPath != "/site/repair.yaml" || !readOnly {
			return errors.New("incorrect HTTP mount arguments")
		}
		progress("launching /var/lib/pvedisk/private-appliance " + strings.Repeat("a", 64))
		progress("mounted")
		mounted.Store(true)
		return nil
	}
	server := httptest.NewServer(Handler(newServer("/site/repair.yaml", ops), nil))
	t.Cleanup(server.Close)
	notifications := make(chan *mcp.ProgressNotificationParams, 4)
	client := lifecycleHTTPClient(t, server.URL, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, request *mcp.ProgressNotificationClientRequest) {
			notifications <- request.Params
		},
	})
	params := &mcp.CallToolParams{Name: "mount", Arguments: map[string]any{"vmid": 400, "read_only": true}}
	params.SetProgressToken("http-mount-progress")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, params)
	if err != nil || result.IsError || !mounted.Load() {
		t.Fatalf("HTTP mount result=%+v err=%v mounted=%v", result, err, mounted.Load())
	}
	assertHTTPLifecycleRedacted(t, resultJSON(t, result))
	for expected := 1; expected <= 2; expected++ {
		select {
		case notification := <-notifications:
			if notification.ProgressToken != "http-mount-progress" || notification.Progress != float64(expected) {
				t.Fatalf("unexpected HTTP progress: %+v", notification)
			}
			assertHTTPLifecycleRedacted(t, notification.Message)
		case <-ctx.Done():
			t.Fatal("HTTP progress notification did not arrive")
		}
	}
}

func TestHTTPCanceledMountPreservesExplicitCleanup(t *testing.T) {
	ops := fixtureOps(t)
	entered, canceled := make(chan struct{}), make(chan struct{})
	ops.mount = func(ctx context.Context, _ int, _ string, _ bool, _ func(string)) error {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}
	var unmounts, recoveries atomic.Int32
	ops.unmount = func(context.Context, int) error { unmounts.Add(1); return nil }
	ops.recover = func(context.Context, int) error { recoveries.Add(1); return nil }
	server := httptest.NewServer(Handler(newServer("/site/repair.yaml", ops), nil))
	t.Cleanup(server.Close)
	client := lifecycleHTTPClient(t, server.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "mount", Arguments: map[string]any{"vmid": 400}})
		done <- err
	}()
	lifecycleHTTPAwait(t, entered, "HTTP mount entry")
	cancel()
	lifecycleHTTPAwait(t, canceled, "HTTP mount context cancellation")
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled HTTP call returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled HTTP request did not return")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	reconnected := lifecycleHTTPClient(t, server.URL, nil)
	status := lifecycleHTTPCall(t, reconnected, "status", map[string]any{"vmid": 400})
	if status.IsError || !strings.Contains(resultJSON(t, status), `"status":"mounted"`) {
		t.Fatalf("status after cancellation: %s", resultJSON(t, status))
	}
	assertHTTPLifecycleRedacted(t, resultJSON(t, status))
	if unmounts.Load() != 0 || recoveries.Load() != 0 {
		t.Fatal("canceling or disconnecting HTTP implicitly cleaned up the repair session")
	}
	if result := lifecycleHTTPCall(t, reconnected, "unmount", map[string]any{"vmid": 400}); result.IsError || unmounts.Load() != 1 {
		t.Fatalf("explicit HTTP cleanup failed: %s", resultJSON(t, result))
	}
}

func TestHTTPDisconnectAfterMountDoesNotUnmount(t *testing.T) {
	ops := fixtureOps(t)
	var mounted atomic.Bool
	var unmounts, recoveries atomic.Int32
	ops.mount = func(context.Context, int, string, bool, func(string)) error { mounted.Store(true); return nil }
	ops.unmount = func(context.Context, int) error { unmounts.Add(1); mounted.Store(false); return nil }
	ops.recover = func(context.Context, int) error { recoveries.Add(1); return nil }
	ops.describe = func(id int) (*session.Description, error) {
		row := fixtureDescription()
		row.VMID = id
		if !mounted.Load() {
			row.State = nil
			row.Observation = session.Observation{Status: "not-mounted", MountsKnown: true, WorkerKnown: true}
		}
		return &row, nil
	}
	server := httptest.NewServer(Handler(newServer("/site/repair.yaml", ops), nil))
	t.Cleanup(server.Close)
	client := lifecycleHTTPClient(t, server.URL, nil)
	if result := lifecycleHTTPCall(t, client, "mount", map[string]any{"vmid": 400}); result.IsError {
		t.Fatalf("HTTP mount failed: %s", resultJSON(t, result))
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	reconnected := lifecycleHTTPClient(t, server.URL, nil)
	status := lifecycleHTTPCall(t, reconnected, "status", map[string]any{"vmid": 400})
	if status.IsError || !mounted.Load() || !strings.Contains(resultJSON(t, status), `"status":"mounted"`) {
		t.Fatalf("disconnect changed mounted state: %s", resultJSON(t, status))
	}
	if unmounts.Load() != 0 || recoveries.Load() != 0 {
		t.Fatal("HTTP disconnect triggered implicit repair cleanup")
	}
	if result := lifecycleHTTPCall(t, reconnected, "unmount", map[string]any{"vmid": 400}); result.IsError {
		t.Fatalf("explicit HTTP unmount failed: %s", resultJSON(t, result))
	}
	if mounted.Load() || unmounts.Load() != 1 || recoveries.Load() != 0 {
		t.Fatal("explicit HTTP unmount did not provide the only cleanup")
	}
}

func TestHTTPCanceledQueuedMountNeverReachesBackend(t *testing.T) {
	ops := fixtureOps(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var mounts atomic.Int32
	ops.mount = func(ctx context.Context, _ int, _ string, _ bool, _ func(string)) error {
		if mounts.Add(1) != 1 {
			return errors.New("canceled queued mount reached the backend")
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	queued, queueFinished := make(chan struct{}), make(chan struct{})
	handler := Handler(newServer("/site/repair.yaml", ops), nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var request struct {
				Params struct {
					Meta struct {
						ProgressToken string `json:"progressToken"`
					} `json:"_meta"`
				} `json:"params"`
			}
			if json.Unmarshal(body, &request) == nil && request.Params.Meta.ProgressToken == "queued-cancellation" {
				close(queued)
				defer close(queueFinished)
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	client := lifecycleHTTPClient(t, server.URL, nil)
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer firstCancel()
	first := make(chan error, 1)
	go func() {
		result, err := client.CallTool(firstCtx, &mcp.CallToolParams{Name: "mount", Arguments: map[string]any{"vmid": 400}})
		if err == nil && result.IsError {
			err = errors.New("initial HTTP mount failed")
		}
		first <- err
	}()
	lifecycleHTTPAwait(t, entered, "first HTTP mount entry")
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	defer cancelQueued()
	second := make(chan error, 1)
	go func() {
		params := &mcp.CallToolParams{Name: "mount", Arguments: map[string]any{"vmid": 400}}
		params.SetProgressToken("queued-cancellation")
		_, err := client.CallTool(queuedCtx, params)
		second <- err
	}()
	lifecycleHTTPAwait(t, queued, "queued HTTP mount arrival")
	cancelQueued()
	lifecycleHTTPAwait(t, queueFinished, "queued HTTP request cancellation")
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued canceled HTTP call did not return")
	}
	close(release)
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("initial HTTP mount did not finish")
	}
	if mounts.Load() != 1 {
		t.Fatalf("canceled queue invoked mount %d times", mounts.Load())
	}
}

func TestHTTPBackendErrorsStayRedactedToolErrors(t *testing.T) {
	ops := fixtureOps(t)
	privateError := errors.New("busy /var/lib/pvedisk/private-worker " + strings.Repeat("a", 64))
	ops.mount = func(context.Context, int, string, bool, func(string)) error { return privateError }
	ops.unmount = func(context.Context, int) error { return privateError }
	ops.recover = func(context.Context, int) error { return privateError }
	server := httptest.NewServer(Handler(newServer("/site/repair.yaml", ops), nil))
	t.Cleanup(server.Close)
	client := lifecycleHTTPClient(t, server.URL, nil)
	for _, name := range []string{"mount", "unmount", "recover"} {
		result := lifecycleHTTPCall(t, client, name, map[string]any{"vmid": 400})
		if !result.IsError {
			t.Fatalf("%s backend failure was not an MCP tool error", name)
		}
		assertHTTPLifecycleRedacted(t, resultJSON(t, result))
	}
}

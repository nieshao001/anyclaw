package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1024XEngineer/anyclaw/pkg/config"
	"github.com/1024XEngineer/anyclaw/pkg/runtime"
)

func newTestMainRuntime(t *testing.T) *runtime.MainRuntime {
	t.Helper()

	workDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = 0
	cfg.Agent.Name = "main"

	return &runtime.MainRuntime{
		ConfigPath: filepath.Join(workDir, "anyclaw.json"),
		Config:     cfg,
		WorkDir:    workDir,
		WorkingDir: workDir,
	}
}

func TestNewGatewayDefaults(t *testing.T) {
	runtimeRef := newTestMainRuntime(t)
	server := New(runtimeRef)
	if server == nil {
		t.Fatal("expected server")
	}
	if server.mainRuntime != runtimeRef {
		t.Fatalf("expected runtime reference to be retained")
	}
	if got := server.address(); got != runtime.GatewayAddress(runtimeRef.Config) {
		t.Fatalf("expected runtime address %q, got %q", runtime.GatewayAddress(runtimeRef.Config), got)
	}
}

func TestGatewayAddressValidation(t *testing.T) {
	if got := (*Server)(nil).address(); got != defaultGatewayAddress {
		t.Fatalf("expected nil server to use default address, got %q", got)
	}
	if got := (&Server{}).address(); got != defaultGatewayAddress {
		t.Fatalf("expected empty server to use default address, got %q", got)
	}
}

func TestGatewayRunNilServer(t *testing.T) {
	var server *Server
	if err := server.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("expected nil server error, got %v", err)
	}
}

func TestGatewayRunReportsListenFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	runtimeRef := newTestMainRuntime(t)
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	runtimeRef.Config.Gateway.Host = host
	runtimeRef.Config.Gateway.Port = parseIntParam(port, 0)
	server := New(runtimeRef)
	err = server.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gateway server failed") {
		t.Fatalf("expected listen failure, got %v", err)
	}
}

func TestGatewayRunStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	server := New(newTestMainRuntime(t))
	if err := server.Run(ctx); err != nil {
		t.Fatalf("run with canceled context: %v", err)
	}
	if server.httpServer == nil {
		t.Fatal("expected http server to be initialized")
	}
	if server.startedAt.IsZero() {
		t.Fatal("expected startedAt to be set")
	}
}

func TestWriteJSONSuccessAndEncodeError(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusCreated, map[string]any{"ok": true})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected json content type, got %q", got)
	}
	var payload map[string]bool
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload["ok"] {
		t.Fatalf("expected ok payload, got %v", payload)
	}

	recorder = httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, make(chan int))
	if !strings.Contains(recorder.Body.String(), "unsupported type") {
		t.Fatalf("expected encode error response, got %q", recorder.Body.String())
	}
}

func TestParseIntParam(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultVal int
		want       int
	}{
		{name: "empty", input: "", defaultVal: 10, want: 10},
		{name: "valid", input: "25", defaultVal: 10, want: 25},
		{name: "zero", input: "0", defaultVal: 10, want: 10},
		{name: "negative", input: "-1", defaultVal: 10, want: 10},
		{name: "invalid", input: "abc", defaultVal: 10, want: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseIntParam(tt.input, tt.defaultVal); got != tt.want {
				t.Fatalf("parseIntParam(%q, %d) = %d, want %d", tt.input, tt.defaultVal, got, tt.want)
			}
		})
	}
}

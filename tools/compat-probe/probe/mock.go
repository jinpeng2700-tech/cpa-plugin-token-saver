package probe

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

const maxProviderBody = 2 << 20

type mockProvider struct {
	listener net.Listener
	server   *http.Server

	mu       sync.Mutex
	requests int
	markers  int
	bodies   [][]byte
}

func startMockProvider() (*mockProvider, bool) {
	listener, errListen := net.Listen("tcp4", "127.0.0.1:0")
	if errListen != nil {
		return nil, false
	}
	mock := &mockProvider{listener: listener}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", mock.handleChat)
	mock.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       3 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = mock.server.Serve(listener) }()
	return mock, true
}

func (mock *mockProvider) URL() string {
	return "http://" + mock.listener.Addr().String()
}

func (mock *mockProvider) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = mock.server.Shutdown(ctx)
	_ = mock.listener.Close()
}

func (mock *mockProvider) Snapshot() (int, int) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	return mock.requests, mock.markers
}

func (mock *mockProvider) Reset() {
	mock.mu.Lock()
	mock.requests = 0
	mock.markers = 0
	mock.bodies = nil
	mock.mu.Unlock()
}

func (mock *mockProvider) SingleBody() ([]byte, bool) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.requests != 1 || len(mock.bodies) != 1 {
		return nil, false
	}
	return append([]byte(nil), mock.bodies[0]...), true
}

func (mock *mockProvider) handleChat(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer compat-upstream-key-only" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	raw, errRead := io.ReadAll(io.LimitReader(request.Body, maxProviderBody+1))
	if errRead != nil || len(raw) > maxProviderBody || !json.Valid(raw) {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	mock.mu.Lock()
	mock.requests++
	mock.markers += markerCount(raw)
	mock.bodies = append(mock.bodies[:0], append([]byte(nil), raw...))
	mock.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"id":"chatcmpl-compat","object":"chat.completion","created":0,"model":"compat-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

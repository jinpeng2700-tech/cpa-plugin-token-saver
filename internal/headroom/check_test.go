package headroom

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func validCompressServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/compress" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"messages":[{"role":"user","content":"ping"}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func slowCompressServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"messages":[{"role":"user","content":"ping"}]}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCheckReportsFunctionalSuccess(t *testing.T) {
	server := validCompressServer(t)
	start := time.Now()
	result := Check(context.Background(), server.URL, 500*time.Millisecond)
	if !result.Reachable || result.Outcome != OutcomeApplied || result.Latency <= 0 ||
		result.Latency > time.Since(start) {
		t.Fatalf("result = %#v", result)
	}
}

func TestCheckReportsTimeoutWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	server := slowCompressServer(t, &calls)
	result := Check(context.Background(), server.URL, 100*time.Millisecond)
	if result.Reachable || result.Outcome != OutcomeTimeout || calls.Load() != 1 {
		t.Fatalf("result=%#v calls=%d", result, calls.Load())
	}
}

func TestCheckReportsConnectionRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rawURL := "http://" + listener.Addr().String()
	_ = listener.Close()

	result := Check(context.Background(), rawURL, 100*time.Millisecond)
	if result.Reachable || result.Outcome != OutcomeConnection {
		t.Fatalf("result = %#v", result)
	}
}

func TestCheckReportsCanceledContext(t *testing.T) {
	var calls atomic.Int32
	server := slowCompressServer(t, &calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := Check(ctx, server.URL, 500*time.Millisecond)
	if result.Reachable || result.Outcome != OutcomeNetwork || calls.Load() != 0 {
		t.Fatalf("result = %#v calls = %d", result, calls.Load())
	}
}

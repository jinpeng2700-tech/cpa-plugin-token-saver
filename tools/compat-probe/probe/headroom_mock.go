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

type mockHeadroomMode uint8

const (
	mockHeadroomRewrite mockHeadroomMode = iota
	mockHeadroomTimeout
)

type mockHeadroom struct {
	listener net.Listener
	server   *http.Server

	mu     sync.Mutex
	mode   mockHeadroomMode
	bodies [][]byte
}

func startMockHeadroom() (*mockHeadroom, bool) {
	listener, errListen := net.Listen("tcp4", "127.0.0.1:0")
	if errListen != nil {
		return nil, false
	}
	mock := &mockHeadroom{listener: listener}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/compress", mock.handleCompress)
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

func (mock *mockHeadroom) URL() string {
	return "http://" + mock.listener.Addr().String()
}

func (mock *mockHeadroom) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = mock.server.Shutdown(ctx)
	_ = mock.listener.Close()
}

func (mock *mockHeadroom) SetMode(mode mockHeadroomMode) {
	mock.mu.Lock()
	mock.mode = mode
	mock.bodies = nil
	mock.mu.Unlock()
}

func (mock *mockHeadroom) LastDispatchBody() []byte {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for index := len(mock.bodies) - 1; index >= 0; index-- {
		var envelope struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(mock.bodies[index], &envelope) == nil && envelope.Model != "headroom-health-probe" {
			return append([]byte(nil), mock.bodies[index]...)
		}
	}
	return nil
}

func (mock *mockHeadroom) handleCompress(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw, errRead := io.ReadAll(io.LimitReader(request.Body, maxProviderBody+1))
	if errRead != nil || len(raw) > maxProviderBody {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	var envelope struct {
		Model    string           `json:"model"`
		Messages []map[string]any `json:"messages"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Messages == nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	mock.mu.Lock()
	mode := mock.mode
	if envelope.Model != "headroom-health-probe" {
		mock.bodies = append(mock.bodies[:0], append([]byte(nil), raw...))
	}
	mock.mu.Unlock()
	if mode == mockHeadroomTimeout {
		time.Sleep(250 * time.Millisecond)
	}

	for _, message := range envelope.Messages {
		role, _ := message["role"].(string)
		if role == "system" || role == "developer" {
			continue
		}
		switch content := message["content"].(type) {
		case string:
			if content != "" {
				message["content"] = "headroom-rewritten"
			}
		case []any:
			for _, rawBlock := range content {
				block, _ := rawBlock.(map[string]any)
				if text, ok := block["text"].(string); ok && text != "" {
					block["text"] = "headroom-rewritten"
				}
			}
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"messages": envelope.Messages})
}

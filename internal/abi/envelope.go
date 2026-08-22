// Package abi defines the ABI1/RPC3 contract implemented by the dynamic plugin.
package abi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/config"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/management"
	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/saver"
)

const (
	ABIVersion       uint32 = 1
	RPCSchemaVersion uint32 = 3

	CallStatusOK    = 0
	CallStatusError = 1

	MethodPluginRegister     = "plugin.register"
	MethodPluginReconfigure  = "plugin.reconfigure"
	MethodPluginShutdown     = "plugin.shutdown"
	MethodRequestNormalize   = "request.normalize"
	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"
)

const (
	PluginName       = "Token Saver"
	PluginAuthor     = "Mr.King"
	PluginRepository = "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver"
)

// PluginVersion is a build identity seam. Release builds override it with
// -ldflags -X so the runtime status and root-owned approval describe the same
// artifact rather than relying on a versioned filename alone.
var PluginVersion = "1.1.1"

// Envelope is the RPC3 response wrapper consumed by CLIProxyAPI.
type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error describes a recoverable plugin call failure.
type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// LifecycleRequest is sent by CLIProxyAPI for registration and reconfigure.
// ConfigYAML is base64 encoded by encoding/json because it is a byte slice.
type LifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

// Registration is the plugin.register/plugin.reconfigure result.
type Registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      Metadata                 `json:"metadata"`
	Capabilities  RegistrationCapabilities `json:"capabilities"`
}

// Metadata mirrors the host's SDK metadata field names.
type Metadata struct {
	Name             string
	Version          string
	Author           string
	GitHubRepository string
	Logo             string
	ConfigFields     []ConfigField
}

// ConfigField mirrors the host's management schema for plugin-owned settings.
type ConfigField struct {
	Name        string
	Type        string
	EnumValues  []string
	Description string
}

// RegistrationCapabilities contains only surfaces implemented in U1.
type RegistrationCapabilities struct {
	RequestNormalizer bool `json:"request_normalizer"`
	ManagementAPI     bool `json:"management_api"`
}

type payloadResponse struct {
	Body []byte
}

// Runtime serializes shutdown against active pure-Go dispatch. Pointer copying
// and native allocation happen outside Call, so panic recovery here is limited
// to recoverable Go dispatch failures rather than unsafe-memory faults.
type Runtime struct {
	mu          sync.Mutex
	cond        *sync.Cond
	active      int
	stopped     bool
	lifecycleMu sync.Mutex
	configs     atomic.Pointer[config.Store]
	saver       *saver.Service
	management  *management.Handler
}

// NewRuntime creates a running runtime with a safe-off configuration snapshot.
func NewRuntime() *Runtime {
	runtime := &Runtime{saver: saver.NewService(saver.Options{})}
	runtime.cond = sync.NewCond(&runtime.mu)
	store, _ := config.NewStore(nil)
	runtime.configs.Store(store)
	runtime.management = management.NewHandler(management.Options{
		BuildVersion:   PluginVersion,
		ABIVersion:     ABIVersion,
		RPCSchema:      RPCSchemaVersion,
		Saver:          runtime.saver,
		ConfigSnapshot: runtime.configs.Load,
	})
	return runtime
}

// Call dispatches one RPC method and always returns an RPC3 envelope.
func (r *Runtime) Call(method string, request []byte) (raw []byte, status int) {
	if !r.acquire() {
		return Failure("plugin_shutdown", "plugin has been shut down"), CallStatusError
	}
	defer r.release()

	defer func() {
		if recover() != nil {
			raw = Failure("internal_error", "plugin dispatch failed")
			status = CallStatusError
		}
	}()
	return r.dispatch(method, request)
}

// Shutdown atomically blocks new dispatch and waits for active calls. Response
// buffers remain owned by the host until it calls cliproxyPluginFree.
func (r *Runtime) Shutdown() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.stopped {
		r.stopped = true
	}
	for r.active > 0 {
		r.cond.Wait()
	}
	r.mu.Unlock()
	if r.saver != nil {
		r.saver.Close()
	}
}

// Stopped reports whether shutdown has begun.
func (r *Runtime) Stopped() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// Snapshot returns the current immutable configuration value.
func (r *Runtime) Snapshot() config.Config {
	if r == nil {
		return config.Defaults()
	}
	store := r.configs.Load()
	if store == nil {
		return config.Defaults()
	}
	return store.Snapshot()
}

func (r *Runtime) acquire() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	r.active++
	return true
}

func (r *Runtime) release() {
	r.mu.Lock()
	r.active--
	if r.active == 0 {
		r.cond.Broadcast()
	}
	r.mu.Unlock()
}

func (r *Runtime) dispatch(method string, request []byte) ([]byte, int) {
	switch method {
	case MethodPluginRegister:
		return r.register(request)
	case MethodPluginReconfigure:
		return r.reconfigure(request)
	case MethodRequestNormalize:
		var input saver.Request
		if errDecode := json.Unmarshal(request, &input); errDecode != nil {
			return Failure("invalid_request", "decode request.normalize: "+errDecode.Error()), CallStatusError
		}
		body := input.Body
		if r.saver != nil {
			body = r.saver.Normalize(context.Background(), input)
		}
		return Success(payloadResponse{Body: body}), CallStatusOK
	case MethodManagementRegister:
		if r.management == nil {
			return Failure("runtime_unavailable", "management runtime is unavailable"), CallStatusError
		}
		return Success(r.management.Registration()), CallStatusOK
	case MethodManagementHandle:
		var input management.Request
		if errDecode := json.Unmarshal(request, &input); errDecode != nil {
			return Failure("invalid_request", "management request is invalid"), CallStatusError
		}
		if r.management == nil {
			return Failure("runtime_unavailable", "management runtime is unavailable"), CallStatusError
		}
		r.lifecycleMu.Lock()
		defer r.lifecycleMu.Unlock()
		response := r.management.Handle(context.Background(), input)
		return Success(response), CallStatusOK
	case MethodPluginShutdown:
		return Failure("unsupported_method", "plugin.shutdown must use the native shutdown function"), CallStatusError
	default:
		return Failure("unknown_method", "unknown method: "+method), CallStatusError
	}
}

func (r *Runtime) register(raw []byte) ([]byte, int) {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	request, errDecode := decodeLifecycleRequest(raw)
	if errDecode != nil {
		return Failure("invalid_request", errDecode.Error()), CallStatusError
	}
	store, _ := config.NewStore(request.ConfigYAML)
	// A bad cold configuration deliberately registers with safe-off defaults.
	if r.saver != nil {
		if errConfigure := r.saver.Reconfigure(store.Snapshot()); errConfigure != nil {
			return Failure("invalid_config", "configure token saver runtime"), CallStatusError
		}
	}
	r.configs.Store(store)
	return Success(pluginRegistration()), CallStatusOK
}

func (r *Runtime) reconfigure(raw []byte) ([]byte, int) {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	request, errDecode := decodeLifecycleRequest(raw)
	if errDecode != nil {
		return Failure("invalid_request", errDecode.Error()), CallStatusError
	}
	nextStore, errConfig := config.NewStore(request.ConfigYAML)
	if errConfig != nil {
		return Failure("invalid_config", "configuration is invalid"), CallStatusError
	}
	if r.saver != nil {
		if errConfigure := r.saver.Reconfigure(nextStore.Snapshot()); errConfigure != nil {
			return Failure("invalid_config", "configure token saver runtime"), CallStatusError
		}
	}
	r.configs.Store(nextStore)
	return Success(pluginRegistration()), CallStatusOK
}

func decodeLifecycleRequest(raw []byte) (LifecycleRequest, error) {
	var request LifecycleRequest
	if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
		return LifecycleRequest{}, fmt.Errorf("decode lifecycle request: %w", errDecode)
	}
	if request.SchemaVersion > RPCSchemaVersion {
		return LifecycleRequest{}, fmt.Errorf("RPC schema version %d is not supported", request.SchemaVersion)
	}
	return request, nil
}

func pluginRegistration() Registration {
	return Registration{
		SchemaVersion: RPCSchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          PluginVersion,
			Author:           PluginAuthor,
			GitHubRepository: PluginRepository,
			ConfigFields:     pluginConfigFields(),
		},
		Capabilities: RegistrationCapabilities{
			RequestNormalizer: true,
			ManagementAPI:     true,
		},
	}
}

func pluginConfigFields() []ConfigField {
	return []ConfigField{
		{Name: "rtk_enabled", Type: "boolean", Description: "Enables RTK tool-output compression."},
		{Name: "headroom_enabled", Type: "boolean", Description: "Enables loopback Headroom compression."},
		{Name: "headroom_url", Type: "string", Description: "Headroom HTTP base URL at literal 127.0.0.1 or ::1."},
		{Name: "headroom_timeout_ms", Type: "integer", Description: "Headroom timeout in milliseconds (100-1500)."},
		{Name: "caveman_enabled", Type: "boolean", Description: "Enables the Caveman response-style prompt."},
		{
			Name:        "caveman_level",
			Type:        "enum",
			EnumValues:  []string{"lite", "full", "ultra", "wenyan-lite", "wenyan", "wenyan-ultra"},
			Description: "Selects the Caveman compression level.",
		},
		{Name: "ponytail_enabled", Type: "boolean", Description: "Enables the Ponytail minimal-code prompt."},
		{
			Name:        "ponytail_level",
			Type:        "enum",
			EnumValues:  []string{"lite", "full", "ultra"},
			Description: "Selects the Ponytail strictness level.",
		},
		{Name: "model_allowlist", Type: "array", Description: "Exact, case-sensitive model strings; empty means all models."},
	}
}

// Success marshals a successful RPC3 envelope. Registration/result structs in
// this package are deliberately JSON-safe, so a marshal failure becomes a
// stable internal error envelope rather than a panic crossing the C boundary.
func Success(result any) []byte {
	rawResult, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return Failure("internal_error", "encode plugin result")
	}
	rawEnvelope, errEnvelope := json.Marshal(Envelope{OK: true, Result: rawResult})
	if errEnvelope != nil {
		return []byte(`{"ok":false,"error":{"code":"internal_error","message":"encode plugin envelope"}}`)
	}
	return rawEnvelope
}

// Failure marshals a failed RPC3 envelope.
func Failure(code, message string) []byte {
	if code == "" {
		code = "plugin_error"
	}
	if message == "" {
		message = "plugin call failed"
	}
	raw, errMarshal := json.Marshal(Envelope{OK: false, Error: &Error{Code: code, Message: message}})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"internal_error","message":"encode plugin error"}}`)
	}
	return raw
}

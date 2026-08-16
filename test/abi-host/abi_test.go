package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"unsafe"

	"github.com/router-for-me/cpa-plugin-token-saver/internal/abi"
)

func TestRuntimeRegistrationReconfigureAndNoOpSurfaces(t *testing.T) {
	runtimeState := abi.NewRuntime()

	registerRequest := lifecycleJSON(t, []byte(`
enabled: true
priority: 4
rtk_enabled: true
headroom_timeout_ms: 1500
model_allowlist: [Model.Exact]
future_field: preserved
`))
	rawRegister, registerStatus := runtimeState.Call(abi.MethodPluginRegister, registerRequest)
	if registerStatus != abi.CallStatusOK {
		t.Fatalf("plugin.register status = %d, envelope = %s", registerStatus, rawRegister)
	}
	registerEnvelope := decodeEnvelope(t, rawRegister)
	if !registerEnvelope.OK {
		t.Fatalf("plugin.register envelope = %#v", registerEnvelope)
	}
	var registration abi.Registration
	if errDecode := json.Unmarshal(registerEnvelope.Result, &registration); errDecode != nil {
		t.Fatalf("decode registration: %v", errDecode)
	}
	if registration.SchemaVersion != abi.RPCSchemaVersion {
		t.Fatalf("schema_version = %d", registration.SchemaVersion)
	}
	if registration.Metadata.Name != "Token Saver" || registration.Metadata.Version != "0.1.0-dev" || registration.Metadata.Author != "Mr.King" {
		t.Fatalf("metadata = %#v", registration.Metadata)
	}
	if !registration.Capabilities.RequestNormalizer || !registration.Capabilities.ManagementAPI {
		t.Fatalf("capabilities = %#v", registration.Capabilities)
	}
	fieldNames := make(map[string]bool, len(registration.Metadata.ConfigFields))
	for _, field := range registration.Metadata.ConfigFields {
		fieldNames[field.Name] = true
	}
	for _, want := range []string{
		"rtk_enabled", "headroom_enabled", "headroom_url", "headroom_timeout_ms",
		"caveman_enabled", "caveman_level", "ponytail_enabled", "ponytail_level", "model_allowlist",
	} {
		if !fieldNames[want] {
			t.Errorf("registration missing config field %q", want)
		}
	}
	for _, hostField := range []string{"enabled", "priority", "store"} {
		if fieldNames[hostField] {
			t.Errorf("registration exposed host-owned config field %q", hostField)
		}
	}

	wantSnapshot := runtimeState.Snapshot()
	if !wantSnapshot.RTKEnabled || !wantSnapshot.AllowsModel("Model.Exact") || wantSnapshot.AllowsModel("model.exact") {
		t.Fatalf("registered snapshot = %#v", wantSnapshot)
	}
	if !bytes.Contains(wantSnapshot.RawYAML, []byte("future_field: preserved")) {
		t.Fatalf("registered RawYAML = %q", wantSnapshot.RawYAML)
	}

	rawReconfigure, reconfigureStatus := runtimeState.Call(
		abi.MethodPluginReconfigure,
		lifecycleJSON(t, []byte("rtk_enabled: false\nheadroom_timeout_ms: 1501\n")),
	)
	if reconfigureStatus != abi.CallStatusError {
		t.Fatalf("invalid plugin.reconfigure status = %d", reconfigureStatus)
	}
	reconfigureEnvelope := decodeEnvelope(t, rawReconfigure)
	if reconfigureEnvelope.OK || reconfigureEnvelope.Error == nil || reconfigureEnvelope.Error.Code != "invalid_config" {
		t.Fatalf("invalid plugin.reconfigure envelope = %#v", reconfigureEnvelope)
	}
	if got := runtimeState.Snapshot(); !got.RTKEnabled || !reflectConfigEqual(got, wantSnapshot) {
		t.Fatalf("snapshot after invalid reconfigure = %#v, want LKG %#v", got, wantSnapshot)
	}

	body := []byte{0, 1, 2, 'A', '\n', 255}
	normalizeRequest, errMarshal := json.Marshal(struct{ Body []byte }{Body: body})
	if errMarshal != nil {
		t.Fatalf("marshal normalize request: %v", errMarshal)
	}
	rawNormalize, normalizeStatus := runtimeState.Call(abi.MethodRequestNormalize, normalizeRequest)
	if normalizeStatus != abi.CallStatusOK {
		t.Fatalf("request.normalize status = %d, envelope = %s", normalizeStatus, rawNormalize)
	}
	var normalized struct{ Body []byte }
	normalizeEnvelope := decodeEnvelope(t, rawNormalize)
	if errDecode := json.Unmarshal(normalizeEnvelope.Result, &normalized); errDecode != nil {
		t.Fatalf("decode normalize result: %v", errDecode)
	}
	if !bytes.Equal(normalized.Body, body) {
		t.Fatalf("normalized Body = %v, want byte-identical %v", normalized.Body, body)
	}

	rawManagement, managementStatus := runtimeState.Call(abi.MethodManagementRegister, []byte(`{}`))
	if managementStatus != abi.CallStatusOK {
		t.Fatalf("management.register status = %d, envelope = %s", managementStatus, rawManagement)
	}
	var management struct {
		Routes    []json.RawMessage `json:"routes"`
		Resources []json.RawMessage `json:"resources"`
	}
	managementEnvelope := decodeEnvelope(t, rawManagement)
	if errDecode := json.Unmarshal(managementEnvelope.Result, &management); errDecode != nil {
		t.Fatalf("decode management registration: %v", errDecode)
	}
	if management.Routes == nil || management.Resources == nil || len(management.Routes) != 0 || len(management.Resources) != 0 {
		t.Fatalf("management registration = %#v, want explicit empty lists", management)
	}
}

func TestInvalidColdRegistrationStaysRegisteredSafeOff(t *testing.T) {
	runtimeState := abi.NewRuntime()
	raw, status := runtimeState.Call(
		abi.MethodPluginRegister,
		lifecycleJSON(t, []byte("rtk_enabled: true\ncaveman_level: invalid\n")),
	)
	if status != abi.CallStatusOK || !decodeEnvelope(t, raw).OK {
		t.Fatalf("invalid cold registration status/envelope = %d/%s", status, raw)
	}
	got := runtimeState.Snapshot()
	if got.RTKEnabled || got.HeadroomEnabled || got.CavemanEnabled || got.PonytailEnabled {
		t.Fatalf("invalid cold registration snapshot = %#v, want safe-off", got)
	}
}

func TestBufferPoolOwnsAndFreesEachAllocationOnce(t *testing.T) {
	heap := newFakeHeap()
	pool := abi.NewBufferPool()

	ptr, length, errAllocate := pool.Allocate([]byte("response"), heap.allocate)
	if errAllocate != nil {
		t.Fatalf("Allocate() error = %v", errAllocate)
	}
	if ptr == nil || length != 8 || pool.Outstanding() != 1 {
		t.Fatalf("allocation = %p/%d, outstanding = %d", ptr, length, pool.Outstanding())
	}
	if !pool.Free(ptr, length, heap.free) {
		t.Fatal("first Free() = false")
	}
	if pool.Free(ptr, length, heap.free) {
		t.Fatal("duplicate Free() = true, want safe ignore")
	}
	unknownBlock := []byte{1}
	if pool.Free(unsafe.Pointer(&unknownBlock[0]), 1, heap.free) {
		t.Fatal("unknown Free() = true, want safe ignore")
	}
	if heap.freeCount() != 1 || pool.Outstanding() != 0 {
		t.Fatalf("free count/outstanding = %d/%d, want 1/0", heap.freeCount(), pool.Outstanding())
	}

	oversized := make([]byte, abi.MaxResponseBytes+1)
	if _, _, errAllocate = pool.Allocate(oversized, heap.allocate); errAllocate == nil {
		t.Fatal("oversized Allocate() error = nil")
	}
	if _, errCopy := abi.CopyRequest(nil, 1); errCopy == nil {
		t.Fatal("CopyRequest(nil, 1) error = nil")
	}
	oneByte := byte(0)
	if _, errCopy := abi.CopyRequest(unsafe.Pointer(&oneByte), abi.MaxRequestBytes+1); errCopy == nil {
		t.Fatal("oversized CopyRequest() error = nil")
	}
}

func TestShutdownRejectsNewDispatchAndKeepsOutstandingBuffersFreeable(t *testing.T) {
	heap := newFakeHeap()
	pool := abi.NewBufferPool()
	ptr, length, errAllocate := pool.Allocate([]byte("host-owned"), heap.allocate)
	if errAllocate != nil {
		t.Fatalf("Allocate() error = %v", errAllocate)
	}

	runtimeState := abi.NewRuntime()
	runtimeState.Shutdown()
	runtimeState.Shutdown()
	raw, status := runtimeState.Call(abi.MethodPluginRegister, lifecycleJSON(t, nil))
	if status != abi.CallStatusError {
		t.Fatalf("post-shutdown status = %d", status)
	}
	envelope := decodeEnvelope(t, raw)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "plugin_shutdown" {
		t.Fatalf("post-shutdown envelope = %#v", envelope)
	}
	if pool.Outstanding() != 1 || !pool.Free(ptr, length, heap.free) || heap.freeCount() != 1 {
		t.Fatalf("outstanding buffer was not safely freeable after shutdown")
	}
}

func TestDynamicABIHostSubprocess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("platform limitation: the cgo dlopen ABI host runs in Linux CI; portable runtime/buffer tests cover Windows")
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goTool += ".exe"
	}
	if output, errEnv := exec.Command(goTool, "env", "CGO_ENABLED").Output(); errEnv != nil || string(bytes.TrimSpace(output)) != "1" {
		t.Skip("platform limitation: CGO_ENABLED=1 is required for the dlopen ABI host")
	}

	repoRoot := moduleRoot(t)
	outputDir := t.TempDir()
	pluginPath := filepath.Join(outputDir, "cpa-plugin-token-saver.so")
	hostPath := filepath.Join(outputDir, "abi-host")
	runCommand(t, repoRoot, goTool, "build", "-buildmode=c-shared", "-o", pluginPath, ".")
	runCommand(t, repoRoot, goTool, "build", "-o", hostPath, "./test/abi-host")

	command := exec.Command(hostPath, pluginPath)
	output, errRun := command.CombinedOutput()
	if errRun != nil {
		t.Fatalf("ABI host subprocess failed: %v\n%s", errRun, output)
	}
	var report struct {
		ABIVersion              uint32 `json:"abi_version"`
		RegistrationOK          bool   `json:"registration_ok"`
		NormalizeByteIdentical  bool   `json:"normalize_byte_identical"`
		ManagementListsEmpty    bool   `json:"management_lists_empty"`
		OutstandingSurvivedStop bool   `json:"outstanding_survived_shutdown"`
		PostShutdownCode        string `json:"post_shutdown_code"`
	}
	if errDecode := json.Unmarshal(output, &report); errDecode != nil {
		t.Fatalf("decode ABI host report: %v\n%s", errDecode, output)
	}
	if report.ABIVersion != abi.ABIVersion || !report.RegistrationOK || !report.NormalizeByteIdentical ||
		!report.ManagementListsEmpty || !report.OutstandingSurvivedStop || report.PostShutdownCode != "plugin_shutdown" {
		t.Fatalf("ABI host report = %#v", report)
	}
}

func lifecycleJSON(t *testing.T, configYAML []byte) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(struct {
		ConfigYAML    []byte `json:"config_yaml"`
		SchemaVersion uint32 `json:"schema_version"`
	}{ConfigYAML: configYAML, SchemaVersion: abi.RPCSchemaVersion})
	if errMarshal != nil {
		t.Fatalf("marshal lifecycle request: %v", errMarshal)
	}
	return raw
}

func decodeEnvelope(t *testing.T, raw []byte) abi.Envelope {
	t.Helper()
	var envelope abi.Envelope
	if errDecode := json.Unmarshal(raw, &envelope); errDecode != nil {
		t.Fatalf("decode envelope %q: %v", raw, errDecode)
	}
	return envelope
}

func reflectConfigEqual(left, right interface{}) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

type fakeHeap struct {
	mu     sync.Mutex
	blocks map[unsafe.Pointer][]byte
	frees  int
}

func newFakeHeap() *fakeHeap {
	return &fakeHeap{blocks: make(map[unsafe.Pointer][]byte)}
}

func (h *fakeHeap) allocate(raw []byte) unsafe.Pointer {
	block := bytes.Clone(raw)
	ptr := unsafe.Pointer(&block[0])
	h.mu.Lock()
	h.blocks[ptr] = block
	h.mu.Unlock()
	return ptr
}

func (h *fakeHeap) free(ptr unsafe.Pointer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.blocks[ptr]; exists {
		delete(h.blocks, ptr)
		h.frees++
	}
}

func (h *fakeHeap) freeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.frees
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func runCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	if output, errRun := command.CombinedOutput(); errRun != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, errRun, output)
	}
}

func Example_platformLimitation() {
	fmt.Println("Linux CI runs the cgo dlopen ABI host; portable tests run on Windows.")
	// Output: Linux CI runs the cgo dlopen ABI host; portable tests run on Windows.
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

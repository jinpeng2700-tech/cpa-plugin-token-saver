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

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/abi"
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
	if registration.Metadata.Name != "Token Saver" || registration.Metadata.Version != "1.2.1" || registration.Metadata.Author != "Mr.King" {
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
		Routes []struct {
			Method string `json:"Method"`
			Path   string `json:"Path"`
		} `json:"routes"`
		Resources []struct {
			Method      string `json:"Method"`
			Path        string `json:"Path"`
			Menu        string `json:"Menu"`
			Description string `json:"Description"`
		} `json:"resources"`
	}
	managementEnvelope := decodeEnvelope(t, rawManagement)
	if errDecode := json.Unmarshal(managementEnvelope.Result, &management); errDecode != nil {
		t.Fatalf("decode management registration: %v", errDecode)
	}
	if len(management.Routes) != 4 || len(management.Resources) != 2 {
		t.Fatalf("management registration = %#v, want four authenticated routes and two public resources", management)
	}
	wantRoutes := map[string]string{
		"/plugins/token-saver/status":         "GET",
		"/plugins/token-saver/self-test":      "POST",
		"/plugins/token-saver/dashboard":      "GET",
		"/plugins/token-saver/headroom/check": "POST",
	}
	for _, route := range management.Routes {
		if wantMethod, ok := wantRoutes[route.Path]; !ok || route.Method != wantMethod {
			t.Errorf("unexpected route: %#v", route)
		}
	}
}

func TestRuntimeManagementUsesRealHostFieldNamesAndBase64Bodies(t *testing.T) {
	runtimeState := abi.NewRuntime()
	_, registerStatus := runtimeState.Call(abi.MethodPluginRegister, lifecycleJSON(t, nil))
	if registerStatus != abi.CallStatusOK {
		t.Fatalf("plugin.register status = %d", registerStatus)
	}

	rawRegistration, registrationStatus := runtimeState.Call(abi.MethodManagementRegister, []byte(`{"BasePath":"/v0/management"}`))
	if registrationStatus != abi.CallStatusOK {
		t.Fatalf("management.register status = %d, envelope = %s", registrationStatus, rawRegistration)
	}
	registrationEnvelope := decodeEnvelope(t, rawRegistration)
	for _, want := range [][]byte{
		[]byte(`"routes"`), []byte(`"resources"`), []byte(`"Method":"GET"`),
		[]byte(`"Path":"/plugins/token-saver/status"`), []byte(`"Method":"POST"`),
		[]byte(`"Path":"/plugins/token-saver/self-test"`),
		[]byte(`"Path":"/plugins/token-saver/dashboard"`),
		[]byte(`"Path":"/plugins/token-saver/headroom/check"`),
		[]byte(`"Path":"/headroom"`),
		[]byte(`"Path":"/headroom/status"`),
	} {
		if !bytes.Contains(registrationEnvelope.Result, want) {
			t.Errorf("management.register result %s missing %s", registrationEnvelope.Result, want)
		}
	}
	for _, forbidden := range [][]byte{[]byte(`"method"`), []byte(`"path"`), []byte(`"Handler"`)} {
		if bytes.Contains(registrationEnvelope.Result, forbidden) {
			t.Errorf("management.register result %s contains %s", registrationEnvelope.Result, forbidden)
		}
	}

	hostRequest := []byte(`{"Method":"GET","Path":"/v0/management/plugins/token-saver/status","Headers":{"Authorization":["TOP_SECRET_SENTINEL"]},"Query":{"raw":["TOP_SECRET_SENTINEL"]},"Body":"VE9QX1NFQ1JFVF9TRU5USU5FTA==","host_callback_id":"callback-1"}`)
	rawHandle, handleStatus := runtimeState.Call(abi.MethodManagementHandle, hostRequest)
	if handleStatus != abi.CallStatusOK {
		t.Fatalf("management.handle status = %d, envelope = %s", handleStatus, rawHandle)
	}
	handleEnvelope := decodeEnvelope(t, rawHandle)
	for _, want := range [][]byte{[]byte(`"StatusCode":200`), []byte(`"Headers"`), []byte(`"Body":"`)} {
		if !bytes.Contains(handleEnvelope.Result, want) {
			t.Errorf("management.handle result %s missing %s", handleEnvelope.Result, want)
		}
	}
	if bytes.Contains(handleEnvelope.Result, []byte("TOP_SECRET_SENTINEL")) {
		t.Fatalf("management response leaked request data: %s", handleEnvelope.Result)
	}
	var response struct {
		StatusCode int
		Headers    map[string][]string
		Body       []byte
	}
	if errDecode := json.Unmarshal(handleEnvelope.Result, &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	if response.StatusCode != 200 || !json.Valid(response.Body) || response.Headers["Content-Type"][0] != "application/json" {
		t.Fatalf("management response = %#v body=%s", response, response.Body)
	}

	invalidRaw, invalidStatus := runtimeState.Call(
		abi.MethodPluginReconfigure,
		lifecycleJSON(t, []byte("headroom_url: http://TOP_SECRET_INVALID.example\n")),
	)
	if invalidStatus != abi.CallStatusError || bytes.Contains(invalidRaw, []byte("TOP_SECRET_INVALID")) {
		t.Fatalf("invalid reconfigure status/envelope = %d/%s", invalidStatus, invalidRaw)
	}
	invalidEnvelope := decodeEnvelope(t, invalidRaw)
	if invalidEnvelope.Error == nil || invalidEnvelope.Error.Code != "invalid_config" || invalidEnvelope.Error.Message != "configuration is invalid" {
		t.Fatalf("invalid reconfigure envelope = %#v", invalidEnvelope)
	}
	rawAfter, afterStatus := runtimeState.Call(abi.MethodManagementHandle, hostRequest)
	if afterStatus != abi.CallStatusOK {
		t.Fatalf("post-invalid management.handle status = %d, envelope = %s", afterStatus, rawAfter)
	}
	afterEnvelope := decodeEnvelope(t, rawAfter)
	var afterResponse struct{ Body []byte }
	if errDecode := json.Unmarshal(afterEnvelope.Result, &afterResponse); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !bytes.Equal(afterResponse.Body, response.Body) {
		t.Fatalf("invalid hot reload changed LKG status:\nbefore=%s\nafter=%s", response.Body, afterResponse.Body)
	}
}

func TestInvalidColdRegistrationPublishesSafeOffConfigErrorStatus(t *testing.T) {
	runtimeState := abi.NewRuntime()
	rawRegister, registerStatus := runtimeState.Call(
		abi.MethodPluginRegister,
		lifecycleJSON(t, []byte("rtk_enabled: true\nheadroom_url: http://TOP_SECRET_INVALID.example\n")),
	)
	if registerStatus != abi.CallStatusOK || !decodeEnvelope(t, rawRegister).OK {
		t.Fatalf("invalid cold registration status/envelope = %d/%s", registerStatus, rawRegister)
	}
	rawStatus, statusCode := runtimeState.Call(abi.MethodManagementHandle, []byte(`{"Method":"GET","Path":"/v0/management/plugins/token-saver/status"}`))
	if statusCode != abi.CallStatusOK {
		t.Fatalf("management.handle status = %d, envelope = %s", statusCode, rawStatus)
	}
	envelope := decodeEnvelope(t, rawStatus)
	var response struct {
		Body []byte
	}
	if errDecode := json.Unmarshal(envelope.Result, &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !bytes.Contains(response.Body, []byte(`"config":"config_error"`)) || !bytes.Contains(response.Body, []byte(`"config_generation":1`)) ||
		!bytes.Contains(response.Body, []byte(`"config_digest":"2446d6019932b9bcade78655430e0befd195fbe272eb2c4c05a89889d5968f1d"`)) ||
		!bytes.Contains(response.Body, []byte(`"pipeline":"all_bypassed"`)) {
		t.Fatalf("cold-invalid status body = %s", response.Body)
	}
	if bytes.Contains(response.Body, []byte("TOP_SECRET_INVALID")) {
		t.Fatalf("cold-invalid status leaked configuration: %s", response.Body)
	}
}

func TestRuntimeRequestNormalizeDispatchesFullTransformRequest(t *testing.T) {
	runtimeState := abi.NewRuntime()
	_, registerStatus := runtimeState.Call(
		abi.MethodPluginRegister,
		lifecycleJSON(t, []byte("caveman_enabled: true\ncaveman_level: lite\nmodel_allowlist: [model-a]\n")),
	)
	if registerStatus != abi.CallStatusOK {
		t.Fatalf("plugin.register status = %d", registerStatus)
	}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	rawRequest, errMarshal := json.Marshal(struct {
		FromFormat string
		ToFormat   string
		Model      string
		Stream     bool
		Body       []byte
	}{FromFormat: "openai", ToFormat: "openai", Model: "model-a", Stream: true, Body: body})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}

	raw, status := runtimeState.Call(abi.MethodRequestNormalize, rawRequest)
	if status != abi.CallStatusOK {
		t.Fatalf("request.normalize status = %d, envelope = %s", status, raw)
	}
	var response struct{ Body []byte }
	envelope := decodeEnvelope(t, raw)
	if !envelope.OK {
		t.Fatalf("request.normalize envelope = %#v", envelope)
	}
	if errDecode := json.Unmarshal(envelope.Result, &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !bytes.Contains(response.Body, []byte("CPA_TOKEN_SAVER_CAVEMAN_START")) {
		t.Fatalf("request.normalize did not dispatch the full request: %s", response.Body)
	}

	malformedRequest, _ := json.Marshal(struct {
		FromFormat string
		ToFormat   string
		Model      string
		Body       []byte
	}{FromFormat: "openai", ToFormat: "openai", Model: "model-a", Body: []byte(`{"messages":`)})
	failOpenRaw, failOpenStatus := runtimeState.Call(abi.MethodRequestNormalize, malformedRequest)
	if failOpenStatus != abi.CallStatusOK {
		t.Fatalf("recoverable normalize failure status = %d, envelope = %s", failOpenStatus, failOpenRaw)
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
	pluginPath, hostPath := buildDynamicABIArtifacts(t)
	report, errRun := runDynamicABIHost(hostPath, pluginPath)
	if errRun != nil {
		t.Fatal(errRun)
	}
	requireDynamicABIReport(t, report)
}

func TestDynamicABIHostSubprocessStress(t *testing.T) {
	pluginPath, hostPath := buildDynamicABIArtifacts(t)
	const processes = 16
	errCh := make(chan error, processes)
	var wg sync.WaitGroup
	for index := 0; index < processes; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report, errRun := runDynamicABIHost(hostPath, pluginPath)
			if errRun == nil {
				errRun = validateDynamicABIReport(report)
			}
			errCh <- errRun
		}()
	}
	wg.Wait()
	close(errCh)
	for errRun := range errCh {
		if errRun != nil {
			t.Fatal(errRun)
		}
	}
}

type dynamicABIReport struct {
	ABIVersion              uint32 `json:"abi_version"`
	RegistrationOK          bool   `json:"registration_ok"`
	NormalizeByteIdentical  bool   `json:"normalize_byte_identical"`
	ManagementRoutesOK      bool   `json:"management_routes_ok"`
	ManagementHandleOK      bool   `json:"management_handle_ok"`
	OutstandingSurvivedStop bool   `json:"outstanding_survived_shutdown"`
	PostShutdownCode        string `json:"post_shutdown_code"`
}

func buildDynamicABIArtifacts(t *testing.T) (string, string) {
	t.Helper()
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
	return pluginPath, hostPath
}

func runDynamicABIHost(hostPath, pluginPath string) (dynamicABIReport, error) {
	command := exec.Command(hostPath, pluginPath)
	output, errRun := command.CombinedOutput()
	if errRun != nil {
		return dynamicABIReport{}, fmt.Errorf("ABI host subprocess failed: %w\n%s", errRun, output)
	}
	var report dynamicABIReport
	if errDecode := json.Unmarshal(output, &report); errDecode != nil {
		return dynamicABIReport{}, fmt.Errorf("decode ABI host report: %w\n%s", errDecode, output)
	}
	return report, nil
}

func requireDynamicABIReport(t *testing.T, report dynamicABIReport) {
	t.Helper()
	if errReport := validateDynamicABIReport(report); errReport != nil {
		t.Fatal(errReport)
	}
}

func validateDynamicABIReport(report dynamicABIReport) error {
	if report.ABIVersion != abi.ABIVersion || !report.RegistrationOK || !report.NormalizeByteIdentical ||
		!report.ManagementRoutesOK || !report.ManagementHandleOK || !report.OutstandingSurvivedStop || report.PostShutdownCode != "plugin_shutdown" {
		return fmt.Errorf("ABI host report = %#v", report)
	}
	return nil
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

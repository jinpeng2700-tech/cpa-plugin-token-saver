package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	pluginconfig "github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	compatClientKey     = "compat-client-key-only"
	compatManagementKey = "compat-management-key-only"
	compatModel         = "compat-model"
	maxPluginBytes      = 512 << 20
	maxHTTPBody         = 2 << 20
)

var requiredScenarios = []string{
	"all-off",
	"rtk",
	"headroom-rewrite",
	"headroom-timeout",
	"caveman",
	"ponytail",
	"fixed-order",
}

type candidateProcess struct {
	command *exec.Cmd
	done    chan error
}

type managementStatus struct {
	BuildVersion     string `json:"build_version"`
	ABIVersion       uint32 `json:"abi_version"`
	RPCSchema        uint32 `json:"rpc_schema"`
	FixtureRevision  string `json:"fixture_revision"`
	Live             bool   `json:"live"`
	Config           string `json:"config"`
	ConfigGeneration uint64 `json:"config_generation"`
	ConfigDigest     string `json:"config_digest"`
}

type selfTestResponse struct {
	FixtureRevision string `json:"fixture_revision"`
	Result          string `json:"result"`
}

type pluginList struct {
	Plugins []struct {
		ID               string `json:"id"`
		Registered       bool   `json:"registered"`
		EffectiveEnabled bool   `json:"effective_enabled"`
		Metadata         *struct {
			Version string `json:"version"`
		} `json:"metadata"`
	} `json:"plugins"`
}

type httpOutcome uint8

const (
	httpOK httpOutcome = iota
	httpAuth
	httpFailed
)

func Run(parent context.Context, options Options) Report {
	mode := options.Mode
	if mode == "" {
		mode = ModePlugin
	}
	if mode != ModePlugin && mode != ModeCoreOnly {
		return failure(CodeModeInvalid)
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if !regularFile(options.CandidatePath) {
		return failure(CodeCandidateInvalid)
	}
	if mode == ModeCoreOnly {
		return runCoreOnly(ctx, options.CandidatePath)
	}
	if !regularFile(options.PluginPath) {
		return failure(CodePluginInvalid)
	}
	pluginID, _, okFilename := parsePluginFilename(options.PluginPath)
	if !okFilename || pluginID != RequiredPlugin {
		return failure(CodePluginIdentity)
	}

	temporaryRoot, errTemp := os.MkdirTemp("", "token-saver-compat-")
	if errTemp != nil {
		return failure(CodeTemporaryState)
	}
	defer os.RemoveAll(temporaryRoot)

	mock, okMock := startMockProvider()
	if !okMock {
		return failure(CodeTemporaryState)
	}
	defer mock.Close()
	headroomMock, okHeadroomMock := startMockHeadroom()
	if !okHeadroomMock {
		return failure(CodeTemporaryState)
	}
	defer headroomMock.Close()

	pluginsDir := filepath.Join(temporaryRoot, "plugins")
	pluginInstallDir := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(pluginInstallDir, 0o700); errMkdir != nil {
		return failure(CodeTemporaryState)
	}
	installedPlugin := filepath.Join(pluginInstallDir, filepath.Base(options.PluginPath))
	if !copyBoundedFile(options.PluginPath, installedPlugin, maxPluginBytes, 0o500) {
		return failure(CodeTemporaryState)
	}
	port, okPort := ephemeralLoopbackPort()
	if !okPort {
		return failure(CodeTemporaryState)
	}
	configPath := filepath.Join(temporaryRoot, "config.yaml")
	if !writeCandidateConfig(configPath, temporaryRoot, pluginsDir, port, mock.URL(), headroomMock.URL(), true) {
		return failure(CodeTemporaryState)
	}

	process, okStart := startCandidate(options.CandidatePath, configPath)
	if !okStart {
		return failure(CodeCandidateStart)
	}
	defer process.Stop()
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	client := localHTTPClient()
	if code := waitForCandidate(ctx, client, baseURL, process); code != CodeOK {
		return failure(code)
	}

	var plugins pluginList
	if outcome := jsonRequest(ctx, client, http.MethodGet, baseURL+"/v0/management/plugins", compatManagementKey, nil, &plugins); outcome != httpOK {
		return failure(httpCode(outcome, CodePluginList))
	}
	pluginState, found := findPlugin(plugins)
	if !found || !pluginState.Registered {
		return failure(CodePluginNotRegistered)
	}
	if !pluginState.EffectiveEnabled {
		return failure(CodePluginNotEffective)
	}
	if pluginState.Version != RequiredVersion {
		return failure(CodePluginVersion)
	}

	statusURL := baseURL + "/v0/management/plugins/token-saver/status"
	var initialStatus managementStatus
	if outcome := jsonRequest(ctx, client, http.MethodGet, statusURL, compatManagementKey, nil, &initialStatus); outcome != httpOK {
		return failure(httpCode(outcome, CodeStatus))
	}
	if code := statusIdentityCode(initialStatus); code != CodeOK {
		return failure(code)
	}
	configURL := baseURL + "/v0/management/plugins/token-saver/config"
	var currentConfig map[string]any
	if outcome := jsonRequest(ctx, client, http.MethodGet, configURL, compatManagementKey, nil, &currentConfig); outcome != httpOK {
		return failure(httpCode(outcome, CodeConfigGet))
	}
	appliedStatus, scenarios, failedScenario, scenarioCode := runRequiredScenarios(
		ctx, client, baseURL, configURL, statusURL, process, currentConfig, initialStatus, mock, headroomMock,
	)
	if scenarioCode != CodeOK {
		return Report{
			SchemaVersion:  SchemaVersion,
			Code:           scenarioCode,
			PluginID:       RequiredPlugin,
			PluginVersion:  pluginState.Version,
			Scenarios:      scenarios,
			FailedScenario: failedScenario,
		}
	}

	var selfTest selfTestResponse
	if outcome := jsonRequest(ctx, client, http.MethodPost, baseURL+"/v0/management/plugins/token-saver/self-test", compatManagementKey, []byte(`{}`), &selfTest); outcome != httpOK ||
		selfTest.Result != "passed" || selfTest.FixtureRevision != "v1" {
		return failure(httpCode(outcome, CodeSelfTest))
	}
	return Report{
		SchemaVersion: SchemaVersion, Compatible: true, Code: CodeOK,
		PluginID: RequiredPlugin, PluginVersion: pluginState.Version, MarkerCount: 1,
		ConfigGeneration: appliedStatus.ConfigGeneration, ConfigDigest: appliedStatus.ConfigDigest,
		Scenarios: scenarios,
	}
}

func runRequiredScenarios(
	ctx context.Context,
	client *http.Client,
	baseURL, configURL, statusURL string,
	process *candidateProcess,
	currentConfig map[string]any,
	initialStatus managementStatus,
	provider *mockProvider,
	headroomMock *mockHeadroom,
) (managementStatus, []string, string, string) {
	status := initialStatus
	completed := make([]string, 0, len(requiredScenarios))
	failureCode := CodeScenario
	run := func(name string, mode mockHeadroomMode, patch map[string]any, body []byte, verify func([]byte, []byte) bool) bool {
		headroomMock.SetMode(mode)
		nextStatus, code := applyProbeConfig(ctx, client, configURL, statusURL, process, currentConfig, status, patch)
		if code != CodeOK {
			status = nextStatus
			failureCode = code
			return false
		}
		status = nextStatus
		provider.Reset()
		headroomMock.SetMode(mode)
		providerBody, codeDispatch := dispatchProbe(ctx, client, baseURL, provider, body)
		if codeDispatch != CodeOK {
			failureCode = codeDispatch
			return false
		}
		if !verify(providerBody, headroomMock.LastDispatchBody()) {
			return false
		}
		completed = append(completed, name)
		return true
	}

	allOffBody := simpleChatFixture("all-off-original")
	if !run("all-off", mockHeadroomRewrite, scenarioPatch(headroomMock.URL(), 1500, false, false, false, false), allOffBody,
		func(providerBody, _ []byte) bool {
			return messageContent(providerBody, "user") == "all-off-original" &&
				bytes.Count(providerBody, []byte(CavemanMarker)) == 0 &&
				bytes.Count(providerBody, []byte(PonytailMarker)) == 0
		}) {
		return status, completed, "all-off", failureCode
	}

	rtkBody, rtkOriginal := rtkChatFixture("rtk-user")
	if !run("rtk", mockHeadroomRewrite, scenarioPatch(headroomMock.URL(), 1500, true, false, false, false), rtkBody,
		func(providerBody, _ []byte) bool { return validRTKResult(providerBody, rtkOriginal) }) {
		return status, completed, "rtk", failureCode
	}

	if !run("headroom-rewrite", mockHeadroomRewrite, scenarioPatch(headroomMock.URL(), 1500, false, true, false, false),
		simpleChatFixture("headroom-original"),
		func(providerBody, headroomBody []byte) bool {
			return len(headroomBody) > 0 && messageContent(providerBody, "user") == "headroom-rewritten"
		}) {
		return status, completed, "headroom-rewrite", failureCode
	}

	if !run("headroom-timeout", mockHeadroomTimeout, scenarioPatch(headroomMock.URL(), 100, false, true, false, false),
		simpleChatFixture("headroom-timeout-original"),
		func(providerBody, headroomBody []byte) bool {
			return len(headroomBody) > 0 && messageContent(providerBody, "user") == "headroom-timeout-original"
		}) {
		return status, completed, "headroom-timeout", failureCode
	}

	if !run("caveman", mockHeadroomRewrite, scenarioPatch(headroomMock.URL(), 1500, false, false, true, false),
		simpleChatFixture("caveman-user"),
		func(providerBody, _ []byte) bool {
			return bytes.Count(providerBody, []byte(CavemanMarker)) == 1 &&
				bytes.Count(providerBody, []byte(PonytailMarker)) == 0
		}) {
		return status, completed, "caveman", failureCode
	}

	if !run("ponytail", mockHeadroomRewrite, scenarioPatch(headroomMock.URL(), 1500, false, false, false, true),
		simpleChatFixture("ponytail-user"),
		func(providerBody, _ []byte) bool {
			return bytes.Count(providerBody, []byte(CavemanMarker)) == 0 &&
				bytes.Count(providerBody, []byte(PonytailMarker)) == 1
		}) {
		return status, completed, "ponytail", failureCode
	}

	fixedBody, fixedOriginal := rtkChatFixture("fixed-order-user")
	if !run("fixed-order", mockHeadroomRewrite, scenarioPatch(headroomMock.URL(), 1500, true, true, true, true), fixedBody,
		func(providerBody, headroomBody []byte) bool {
			cavemanIndex := bytes.Index(providerBody, []byte(CavemanMarker))
			ponytailIndex := bytes.Index(providerBody, []byte(PonytailMarker))
			return validRTKResult(headroomBody, fixedOriginal) &&
				messageContent(providerBody, "user") == "headroom-rewritten" &&
				cavemanIndex >= 0 && ponytailIndex > cavemanIndex &&
				bytes.Count(providerBody, []byte(CavemanMarker)) == 1 &&
				bytes.Count(providerBody, []byte(PonytailMarker)) == 1
		}) {
		return status, completed, "fixed-order", failureCode
	}

	return status, completed, "", CodeOK
}

func scenarioPatch(headroomURL string, timeoutMS int, rtkEnabled, headroomEnabled, cavemanEnabled, ponytailEnabled bool) map[string]any {
	return map[string]any{
		"rtk_enabled":         rtkEnabled,
		"headroom_enabled":    headroomEnabled,
		"headroom_url":        headroomURL,
		"headroom_timeout_ms": timeoutMS,
		"caveman_enabled":     cavemanEnabled,
		"caveman_level":       "lite",
		"ponytail_enabled":    ponytailEnabled,
		"ponytail_level":      "lite",
		"model_allowlist":     []string{compatModel},
	}
}

func applyProbeConfig(
	ctx context.Context,
	client *http.Client,
	configURL, statusURL string,
	process *candidateProcess,
	currentConfig map[string]any,
	previousStatus managementStatus,
	patch map[string]any,
) (managementStatus, string) {
	for key, value := range patch {
		currentConfig[key] = value
	}
	desiredRaw, errDesired := json.Marshal(currentConfig)
	if errDesired != nil {
		return previousStatus, CodeConfigPatch
	}
	desiredConfig, errParseConfig := pluginconfig.Parse(desiredRaw)
	if errParseConfig != nil {
		return previousStatus, CodeConfigPatch
	}
	patchRaw, errPatch := json.Marshal(patch)
	if errPatch != nil {
		return previousStatus, CodeConfigPatch
	}
	if outcome := jsonRequest(ctx, client, http.MethodPatch, configURL, compatManagementKey, patchRaw, nil); outcome != httpOK {
		return previousStatus, httpCode(outcome, CodeConfigPatch)
	}
	return waitForAppliedConfig(ctx, client, statusURL, process, previousStatus.ConfigGeneration, pluginconfig.Digest(desiredConfig))
}

func dispatchProbe(ctx context.Context, client *http.Client, baseURL string, provider *mockProvider, body []byte) ([]byte, string) {
	var response map[string]any
	if outcome := jsonRequest(ctx, client, http.MethodPost, baseURL+"/v1/chat/completions", compatClientKey, body, &response); outcome != httpOK {
		return nil, CodeDispatch
	}
	body, ok := provider.SingleBody()
	if !ok {
		return nil, CodeDispatch
	}
	return body, CodeOK
}

func simpleChatFixture(content string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"model": compatModel,
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
		"stream": false,
	})
	return raw
}

func rtkChatFixture(userContent string) ([]byte, string) {
	var diff strings.Builder
	diff.WriteString("diff --git a/compat.go b/compat.go\n--- a/compat.go\n+++ b/compat.go\n@@ -1 +1 @@\n")
	for index := 0; index < 120; index++ {
		fmt.Fprintf(&diff, "+compatibility line %03d %s\n", index, strings.Repeat("x", 32))
	}
	original := diff.String()
	raw, _ := json.Marshal(map[string]any{
		"model": compatModel,
		"messages": []map[string]any{
			{"role": "user", "content": userContent},
			{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]any{
					{"id": "call_ok", "type": "function", "function": map[string]any{"name": "run", "arguments": "{}"}},
					{"id": "call_err", "type": "function", "function": map[string]any{"name": "run", "arguments": "{}"}},
				},
			},
			{"role": "tool", "tool_call_id": "call_ok", "content": original},
			{"role": "tool", "tool_call_id": "call_err", "content": original, "is_error": true},
		},
		"stream": false,
	})
	return raw, original
}

type probeMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	IsError    bool            `json:"is_error"`
}

func probeMessages(raw []byte) ([]probeMessage, bool) {
	var envelope struct {
		Messages []probeMessage `json:"messages"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Messages == nil {
		return nil, false
	}
	return envelope.Messages, true
}

func messageContent(raw []byte, role string) string {
	messages, ok := probeMessages(raw)
	if !ok {
		return ""
	}
	for _, message := range messages {
		if message.Role != role {
			continue
		}
		var content string
		if json.Unmarshal(message.Content, &content) == nil {
			return content
		}
	}
	return ""
}

func validRTKResult(raw []byte, original string) bool {
	messages, ok := probeMessages(raw)
	if !ok {
		return false
	}
	tools := make([]probeMessage, 0, 2)
	for _, message := range messages {
		if message.Role == "tool" {
			tools = append(tools, message)
		}
	}
	if len(tools) != 2 || tools[0].ToolCallID != "call_ok" || tools[1].ToolCallID != "call_err" || tools[0].IsError || !tools[1].IsError {
		return false
	}
	var compressed, failed string
	if json.Unmarshal(tools[0].Content, &compressed) != nil || json.Unmarshal(tools[1].Content, &failed) != nil {
		return false
	}
	return len(compressed) < len(original) && failed == original
}

func runCoreOnly(ctx context.Context, candidatePath string) Report {
	temporaryRoot, errTemp := os.MkdirTemp("", "cliproxyapi-core-compat-")
	if errTemp != nil {
		return failure(CodeTemporaryState)
	}
	defer os.RemoveAll(temporaryRoot)

	mock, okMock := startMockProvider()
	if !okMock {
		return failure(CodeTemporaryState)
	}
	defer mock.Close()

	pluginsDir := filepath.Join(temporaryRoot, "plugins")
	if errMkdir := os.MkdirAll(pluginsDir, 0o700); errMkdir != nil {
		return failure(CodeTemporaryState)
	}
	port, okPort := ephemeralLoopbackPort()
	if !okPort {
		return failure(CodeTemporaryState)
	}
	configPath := filepath.Join(temporaryRoot, "config.yaml")
	if !writeCandidateConfig(configPath, temporaryRoot, pluginsDir, port, mock.URL(), "", false) {
		return failure(CodeTemporaryState)
	}

	process, okStart := startCandidate(candidatePath, configPath)
	if !okStart {
		return failure(CodeCandidateStart)
	}
	defer process.Stop()
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	client := localHTTPClient()
	if code := waitForCandidate(ctx, client, baseURL, process); code != CodeOK {
		return failure(code)
	}

	chatBody := []byte(`{"model":"compat-model","messages":[{"role":"user","content":"bounded core-only compatibility fixture"}],"stream":false}`)
	var chatResponse map[string]any
	if outcome := jsonRequest(ctx, client, http.MethodPost, baseURL+"/v1/chat/completions", compatClientKey, chatBody, &chatResponse); outcome != httpOK {
		return failure(CodeDispatch)
	}
	requestCount, markers := mock.Snapshot()
	if requestCount != 1 {
		return Report{SchemaVersion: SchemaVersion, Code: CodeDispatch, MarkerCount: markers}
	}
	if markers != 0 {
		return Report{SchemaVersion: SchemaVersion, Code: CodeMarkerUnexpected, MarkerCount: markers}
	}
	return Report{SchemaVersion: SchemaVersion, Compatible: true, Code: CodeOK}
}

func regularFile(path string) bool {
	info, errStat := os.Stat(path)
	return errStat == nil && info.Mode().IsRegular()
}

func copyBoundedFile(source, destination string, maximum int64, mode os.FileMode) bool {
	input, errOpen := os.Open(source)
	if errOpen != nil {
		return false
	}
	defer input.Close()
	info, errStat := input.Stat()
	if errStat != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return false
	}
	output, errCreate := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errCreate != nil {
		return false
	}
	_, errCopy := io.Copy(output, input)
	errClose := output.Close()
	return errCopy == nil && errClose == nil
}

func ephemeralLoopbackPort() (int, bool) {
	listener, errListen := net.Listen("tcp4", "127.0.0.1:0")
	if errListen != nil {
		return 0, false
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port, true
}

func startCandidate(candidatePath, configPath string) (*candidateProcess, bool) {
	command := exec.Command(candidatePath, "-config", configPath)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.Env = sanitizedCandidateEnvironment()
	if errStart := command.Start(); errStart != nil {
		return nil, false
	}
	process := &candidateProcess{command: command, done: make(chan error, 1)}
	go func() {
		process.done <- command.Wait()
		close(process.done)
	}()
	return process, true
}

func sanitizedCandidateEnvironment() []string {
	allowed := map[string]struct{}{
		"COMSPEC": {}, "HOME": {}, "LANG": {}, "LC_ALL": {}, "PATH": {}, "PATHEXT": {},
		"SYSTEMROOT": {}, "TEMP": {}, "TMP": {}, "TMPDIR": {}, "TZ": {}, "WINDIR": {},
	}
	environment := make([]string, 0, len(allowed)+2)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if _, keep := allowed[strings.ToUpper(name)]; keep {
			environment = append(environment, item)
		}
	}
	return append(environment, "NO_PROXY=127.0.0.1,::1", "no_proxy=127.0.0.1,::1")
}

func (process *candidateProcess) Stop() {
	if process == nil || process.command == nil || process.command.Process == nil {
		return
	}
	select {
	case <-process.done:
		return
	default:
	}
	_ = process.command.Process.Signal(os.Interrupt)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-process.done:
		return
	case <-timer.C:
		_ = process.command.Process.Kill()
		select {
		case <-process.done:
		case <-time.After(time.Second):
		}
	}
}

func localHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			ResponseHeaderTimeout: 3 * time.Second,
			DisableCompression:    true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func waitForCandidate(ctx context.Context, client *http.Client, baseURL string, process *candidateProcess) string {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-process.done:
			return CodeCandidateExit
		default:
		}
		request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
		if errRequest == nil {
			response, errDo := client.Do(request)
			if errDo == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return CodeOK
				}
			}
		}
		select {
		case <-ctx.Done():
			return CodeCoreTimeout
		case <-process.done:
			return CodeCandidateExit
		case <-ticker.C:
		}
	}
}

func jsonRequest(ctx context.Context, client *http.Client, method, endpoint, credential string, body []byte, target any) httpOutcome {
	request, errRequest := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if errRequest != nil {
		return httpFailed
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, errDo := client.Do(request)
	if errDo != nil {
		return httpFailed
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxHTTPBody+1))
		return httpAuth
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxHTTPBody+1))
		return httpFailed
	}
	raw, errRead := io.ReadAll(io.LimitReader(response.Body, maxHTTPBody+1))
	if errRead != nil || len(raw) > maxHTTPBody {
		return httpFailed
	}
	if target != nil && json.Unmarshal(raw, target) != nil {
		return httpFailed
	}
	return httpOK
}

func httpCode(outcome httpOutcome, fallback string) string {
	if outcome == httpAuth {
		return CodeManagementAuth
	}
	return fallback
}

func findPlugin(list pluginList) (PluginState, bool) {
	for _, plugin := range list.Plugins {
		if plugin.ID != RequiredPlugin {
			continue
		}
		state := PluginState{Registered: plugin.Registered, EffectiveEnabled: plugin.EffectiveEnabled}
		if plugin.Metadata != nil {
			state.Version = plugin.Metadata.Version
		}
		return state, true
	}
	return PluginState{}, false
}

type PluginState struct {
	Registered       bool
	EffectiveEnabled bool
	Version          string
}

func statusIdentityCode(status managementStatus) string {
	if !status.Live || status.Config != "valid" {
		return CodeStatus
	}
	if status.ABIVersion != 1 {
		return CodePluginABI
	}
	if status.RPCSchema != 3 {
		return CodePluginRPC
	}
	if status.FixtureRevision != "v1" {
		return CodePluginFixture
	}
	return CodeOK
}

func waitForAppliedConfig(parent context.Context, client *http.Client, statusURL string, process *candidateProcess, previousGeneration uint64, digest string) (managementStatus, string) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status managementStatus
		if outcome := jsonRequest(ctx, client, http.MethodGet, statusURL, compatManagementKey, nil, &status); outcome == httpAuth {
			return managementStatus{}, CodeManagementAuth
		} else if outcome == httpOK && status.ConfigGeneration > previousGeneration && status.ConfigDigest == digest {
			if code := statusIdentityCode(status); code != CodeOK {
				return managementStatus{}, code
			}
			return status, CodeOK
		}
		select {
		case <-ctx.Done():
			return managementStatus{}, CodeConfigApplyTimeout
		case <-process.done:
			return managementStatus{}, CodeCandidateExit
		case <-ticker.C:
		}
	}
}

func writeCandidateConfig(path, temporaryRoot, pluginsDir string, port int, providerURL, headroomURL string, pluginEnabled bool) bool {
	type remoteManagement struct {
		AllowRemote            bool   `yaml:"allow-remote"`
		SecretKey              string `yaml:"secret-key"`
		DisableControlPanel    bool   `yaml:"disable-control-panel"`
		DisableAutoUpdatePanel bool   `yaml:"disable-auto-update-panel"`
	}
	type pluginHost struct {
		Enabled bool                      `yaml:"enabled"`
		Dir     string                    `yaml:"dir"`
		Configs map[string]map[string]any `yaml:"configs"`
	}
	type compatKey struct {
		APIKey string `yaml:"api-key"`
	}
	type providerModel struct {
		Name  string `yaml:"name"`
		Alias string `yaml:"alias"`
	}
	type compatProvider struct {
		Name          string          `yaml:"name"`
		BaseURL       string          `yaml:"base-url"`
		APIKeyEntries []compatKey     `yaml:"api-key-entries"`
		Models        []providerModel `yaml:"models"`
		RequestRetry  int             `yaml:"request-retry"`
	}
	type hostConfig struct {
		Host                string           `yaml:"host"`
		Port                int              `yaml:"port"`
		AuthDir             string           `yaml:"auth-dir"`
		APIKeys             []string         `yaml:"api-keys"`
		RequestRetry        int              `yaml:"request-retry"`
		CommercialMode      bool             `yaml:"commercial-mode"`
		LoggingToFile       bool             `yaml:"logging-to-file"`
		RequestLog          bool             `yaml:"request-log"`
		RemoteManagement    remoteManagement `yaml:"remote-management"`
		Plugins             pluginHost       `yaml:"plugins"`
		OpenAICompatibility []compatProvider `yaml:"openai-compatibility"`
	}
	pluginConfigs := map[string]map[string]any{}
	if pluginEnabled {
		pluginConfigs[RequiredPlugin] = map[string]any{
			"enabled": true, "priority": -100,
			"rtk_enabled": false, "headroom_enabled": false, "headroom_url": headroomURL, "headroom_timeout_ms": 1500,
			"caveman_enabled": false, "caveman_level": "lite",
			"ponytail_enabled": false, "ponytail_level": "lite",
			"model_allowlist": []string{compatModel},
		}
	}
	configuration := hostConfig{
		Host: "127.0.0.1", Port: port, AuthDir: filepath.Join(temporaryRoot, "auth"),
		APIKeys: []string{compatClientKey}, RequestRetry: 0, CommercialMode: true,
		RemoteManagement: remoteManagement{
			SecretKey: compatManagementKey, DisableControlPanel: true, DisableAutoUpdatePanel: true,
		},
		Plugins: pluginHost{Enabled: pluginEnabled, Dir: pluginsDir, Configs: pluginConfigs},
		OpenAICompatibility: []compatProvider{{
			Name: "compat-mock", BaseURL: providerURL + "/v1",
			APIKeyEntries: []compatKey{{APIKey: "compat-upstream-key-only"}},
			Models:        []providerModel{{Name: compatModel, Alias: compatModel}}, RequestRetry: 0,
		}},
	}
	raw, errMarshal := yaml.Marshal(configuration)
	if errMarshal != nil {
		return false
	}
	return os.WriteFile(path, raw, 0o600) == nil
}

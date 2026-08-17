package probe

import (
	"bytes"
	"context"
	"encoding/json"
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

	pluginconfig "github.com/router-for-me/cpa-plugin-token-saver/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	compatClientKey     = "compat-client-key-only"
	compatManagementKey = "compat-management-key-only"
	compatModel         = "compat-model"
	maxPluginBytes      = 512 << 20
	maxHTTPBody         = 2 << 20
)

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
	if !regularFile(options.PluginPath) {
		return failure(CodePluginInvalid)
	}
	pluginID, filenameVersion, okFilename := parsePluginFilename(options.PluginPath)
	if !okFilename || pluginID != RequiredPlugin || filenameVersion == "" {
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
	if !writeCandidateConfig(configPath, temporaryRoot, pluginsDir, port, mock.URL()) {
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
	currentConfig["caveman_enabled"] = true
	currentConfig["caveman_level"] = "lite"
	desiredRaw, errDesired := json.Marshal(currentConfig)
	if errDesired != nil {
		return failure(CodeConfigPatch)
	}
	desiredConfig, errParseConfig := pluginconfig.Parse(desiredRaw)
	if errParseConfig != nil {
		return failure(CodeConfigPatch)
	}
	desiredDigest := pluginconfig.Digest(desiredConfig)
	patch := []byte(`{"caveman_enabled":true,"caveman_level":"lite"}`)
	if outcome := jsonRequest(ctx, client, http.MethodPatch, configURL, compatManagementKey, patch, nil); outcome != httpOK {
		return failure(httpCode(outcome, CodeConfigPatch))
	}
	appliedStatus, codeApplied := waitForAppliedConfig(ctx, client, statusURL, process, initialStatus.ConfigGeneration, desiredDigest)
	if codeApplied != CodeOK {
		return failure(codeApplied)
	}
	var appliedConfig map[string]any
	if outcome := jsonRequest(ctx, client, http.MethodGet, configURL, compatManagementKey, nil, &appliedConfig); outcome != httpOK ||
		appliedConfig["caveman_enabled"] != true || appliedConfig["caveman_level"] != "lite" {
		return failure(httpCode(outcome, CodeConfigGet))
	}

	chatBody := []byte(`{"model":"compat-model","messages":[{"role":"user","content":"bounded compatibility fixture"}],"stream":false}`)
	var chatResponse map[string]any
	if outcome := jsonRequest(ctx, client, http.MethodPost, baseURL+"/v1/chat/completions", compatClientKey, chatBody, &chatResponse); outcome != httpOK {
		return failure(CodeDispatch)
	}
	requestCount, markers := mock.Snapshot()
	if requestCount != 1 {
		return Report{SchemaVersion: SchemaVersion, Code: CodeDispatch, PluginID: RequiredPlugin, MarkerCount: markers}
	}
	if markers == 0 {
		return Report{SchemaVersion: SchemaVersion, Code: CodeMarkerAbsent, PluginID: RequiredPlugin}
	}
	if markers != 1 {
		return Report{SchemaVersion: SchemaVersion, Code: CodeMarkerDuplicated, PluginID: RequiredPlugin, MarkerCount: markers}
	}

	var selfTest selfTestResponse
	if outcome := jsonRequest(ctx, client, http.MethodPost, baseURL+"/v0/management/plugins/token-saver/self-test", compatManagementKey, []byte(`{}`), &selfTest); outcome != httpOK ||
		selfTest.Result != "passed" || selfTest.FixtureRevision != "v1" {
		return failure(httpCode(outcome, CodeSelfTest))
	}
	return Report{
		SchemaVersion: SchemaVersion, Compatible: true, Code: CodeOK,
		PluginID: RequiredPlugin, PluginVersion: pluginState.Version, MarkerCount: markers,
		ConfigGeneration: appliedStatus.ConfigGeneration, ConfigDigest: appliedStatus.ConfigDigest,
	}
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
	blocked := map[string]struct{}{
		"MANAGEMENT_PASSWORD": {}, "HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {},
		"http_proxy": {}, "https_proxy": {}, "all_proxy": {}, "NO_PROXY": {}, "no_proxy": {},
	}
	environment := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if _, remove := blocked[name]; !remove {
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

func writeCandidateConfig(path, temporaryRoot, pluginsDir string, port int, providerURL string) bool {
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
	configuration := hostConfig{
		Host: "127.0.0.1", Port: port, AuthDir: filepath.Join(temporaryRoot, "auth"),
		APIKeys: []string{compatClientKey}, RequestRetry: 0, CommercialMode: true,
		RemoteManagement: remoteManagement{
			SecretKey: compatManagementKey, DisableControlPanel: true, DisableAutoUpdatePanel: true,
		},
		Plugins: pluginHost{Enabled: true, Dir: pluginsDir, Configs: map[string]map[string]any{
			RequiredPlugin: {
				"enabled": true, "priority": -100,
				"rtk_enabled": false, "headroom_enabled": false,
				"caveman_enabled": false, "caveman_level": "lite",
				"ponytail_enabled": false, "ponytail_level": "lite",
				"model_allowlist": []string{compatModel},
			},
		}},
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

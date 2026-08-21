//go:build linux && cgo

package main

/*
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;
typedef int (*cliproxy_plugin_init_fn)(const cliproxy_host_api*, cliproxy_plugin_api*);

static int host_call(void* ctx, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	(void)ctx; (void)method; (void)request; (void)request_len;
	if (response != NULL) { response->ptr = NULL; response->len = 0; }
	return 1;
}
static void host_free(void* ptr, size_t len) { (void)len; free(ptr); }
static void set_host_api(cliproxy_host_api* api) {
	api->abi_version = 1; api->host_ctx = NULL; api->call = host_call; api->free_buffer = host_free;
}
static void* open_library(const char* path) { return dlopen(path, RTLD_NOW | RTLD_LOCAL); }
static void* find_symbol(void* handle, const char* name) { return dlsym(handle, name); }
static const char* last_error(void) { return dlerror(); }
static int close_library(void* handle) { return dlclose(handle); }
static int call_init(void* fn, const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
	return ((cliproxy_plugin_init_fn)fn)(host, plugin);
}
static int call_plugin(cliproxy_plugin_call_fn fn, char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return fn(method, request, request_len, response);
}
static void free_plugin(cliproxy_plugin_free_fn fn, void* ptr, size_t len) { fn(ptr, len); }
static void shutdown_plugin(cliproxy_plugin_shutdown_fn fn) { fn(); }
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"unsafe"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/internal/abi"
)

type hostReport struct {
	ABIVersion              uint32 `json:"abi_version"`
	RegistrationOK          bool   `json:"registration_ok"`
	NormalizeByteIdentical  bool   `json:"normalize_byte_identical"`
	ManagementRoutesOK      bool   `json:"management_routes_ok"`
	ManagementHandleOK      bool   `json:"management_handle_ok"`
	OutstandingSurvivedStop bool   `json:"outstanding_survived_shutdown"`
	PostShutdownCode        string `json:"post_shutdown_code"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: abi-host <plugin.so>")
		os.Exit(2)
	}
	report, errRun := runHost(os.Args[1])
	if errRun != nil {
		fmt.Fprintln(os.Stderr, errRun)
		os.Exit(1)
	}
	if errEncode := json.NewEncoder(os.Stdout).Encode(report); errEncode != nil {
		fmt.Fprintln(os.Stderr, errEncode)
		os.Exit(1)
	}
}

func runHost(path string) (hostReport, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	handle := C.open_library(cPath)
	if handle == nil {
		return hostReport{}, fmt.Errorf("dlopen: %s", C.GoString(C.last_error()))
	}
	defer C.close_library(handle)
	cName := C.CString("cliproxy_plugin_init")
	initSymbol := C.find_symbol(handle, cName)
	C.free(unsafe.Pointer(cName))
	if initSymbol == nil {
		return hostReport{}, fmt.Errorf("dlsym: %s", C.GoString(C.last_error()))
	}
	var host C.cliproxy_host_api
	var plugin C.cliproxy_plugin_api
	C.set_host_api(&host)
	if rc := C.call_init(initSymbol, &host, &plugin); rc != 0 {
		return hostReport{}, fmt.Errorf("cliproxy_plugin_init returned %d", int(rc))
	}
	if plugin.call == nil || plugin.free_buffer == nil || plugin.shutdown == nil {
		return hostReport{}, fmt.Errorf("plugin function table is incomplete")
	}

	lifecycle, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte("rtk_enabled: true\nfuture_field: preserved\n"),
		SchemaVersion: abi.RPCSchemaVersion,
	})
	registerRaw, registerRC, errRegister := callAndFree(&plugin, abi.MethodPluginRegister, lifecycle, true)
	if errRegister != nil {
		return hostReport{}, errRegister
	}
	var registerEnvelope abi.Envelope
	_ = json.Unmarshal(registerRaw, &registerEnvelope)

	body := []byte{0, 1, 2, 'A', 255}
	normalizeRequest, _ := json.Marshal(struct{ Body []byte }{Body: body})
	normalizeRaw, normalizeRC, errNormalize := callAndFree(&plugin, abi.MethodRequestNormalize, normalizeRequest, false)
	if errNormalize != nil {
		return hostReport{}, errNormalize
	}
	var normalizeEnvelope abi.Envelope
	var normalizeResult struct{ Body []byte }
	_ = json.Unmarshal(normalizeRaw, &normalizeEnvelope)
	_ = json.Unmarshal(normalizeEnvelope.Result, &normalizeResult)

	managementRaw, managementRC, errManagement := callAndFree(&plugin, abi.MethodManagementRegister, []byte(`{}`), false)
	if errManagement != nil {
		return hostReport{}, errManagement
	}
	var managementEnvelope abi.Envelope
	var managementResult struct {
		Routes    []json.RawMessage `json:"routes"`
		Resources []json.RawMessage `json:"resources"`
	}
	_ = json.Unmarshal(managementRaw, &managementEnvelope)
	_ = json.Unmarshal(managementEnvelope.Result, &managementResult)
	managementRoutesOK := managementRC == 0 && managementEnvelope.OK && len(managementResult.Routes) == 2 && len(managementResult.Resources) == 1 &&
		bytes.Contains(managementEnvelope.Result, []byte(`"Method":"GET"`)) && bytes.Contains(managementEnvelope.Result, []byte(`"Path":"/plugins/token-saver/status"`)) &&
		bytes.Contains(managementEnvelope.Result, []byte(`"Method":"POST"`)) && bytes.Contains(managementEnvelope.Result, []byte(`"Path":"/plugins/token-saver/self-test"`))

	managementRequest, _ := json.Marshal(struct {
		Method  string
		Path    string
		Headers map[string][]string
		Query   map[string][]string
		Body    []byte
	}{
		Method:  "GET",
		Path:    "/v0/management/plugins/token-saver/status",
		Headers: map[string][]string{"Authorization": {"TOP_SECRET_SENTINEL"}},
		Query:   map[string][]string{"raw": {"TOP_SECRET_SENTINEL"}},
		Body:    []byte("TOP_SECRET_SENTINEL"),
	})
	managementHandleRaw, managementHandleRC, errManagementHandle := callAndFree(&plugin, abi.MethodManagementHandle, managementRequest, false)
	if errManagementHandle != nil {
		return hostReport{}, errManagementHandle
	}
	var managementHandleEnvelope abi.Envelope
	var managementResponse struct {
		StatusCode int
		Headers    map[string][]string
		Body       []byte
	}
	_ = json.Unmarshal(managementHandleRaw, &managementHandleEnvelope)
	_ = json.Unmarshal(managementHandleEnvelope.Result, &managementResponse)
	managementHandleOK := managementHandleRC == 0 && managementHandleEnvelope.OK && managementResponse.StatusCode == 200 &&
		json.Valid(managementResponse.Body) && !bytes.Contains(managementResponse.Body, []byte("TOP_SECRET_SENTINEL"))

	retained, retainedRC, errRetained := callRetained(&plugin, abi.MethodPluginRegister, lifecycle)
	if errRetained != nil || retainedRC != 0 {
		return hostReport{}, fmt.Errorf("retained response call failed: rc=%d err=%v", retainedRC, errRetained)
	}
	beforeShutdown, errBefore := copyResponse(retained)
	if errBefore != nil {
		return hostReport{}, errBefore
	}
	C.shutdown_plugin(plugin.shutdown)
	afterShutdown, errAfter := copyResponse(retained)
	if errAfter != nil {
		return hostReport{}, errAfter
	}
	C.free_plugin(plugin.free_buffer, retained.ptr, retained.len)
	C.free_plugin(plugin.free_buffer, retained.ptr, retained.len)

	postRaw, postRC, errPost := callAndFree(&plugin, abi.MethodPluginRegister, lifecycle, true)
	if errPost != nil {
		return hostReport{}, errPost
	}
	C.shutdown_plugin(plugin.shutdown)
	var postEnvelope abi.Envelope
	_ = json.Unmarshal(postRaw, &postEnvelope)
	postCode := ""
	if postEnvelope.Error != nil {
		postCode = postEnvelope.Error.Code
	}

	return hostReport{
		ABIVersion:              uint32(plugin.abi_version),
		RegistrationOK:          registerRC == 0 && registerEnvelope.OK,
		NormalizeByteIdentical:  normalizeRC == 0 && normalizeEnvelope.OK && bytes.Equal(normalizeResult.Body, body),
		ManagementRoutesOK:      managementRoutesOK,
		ManagementHandleOK:      managementHandleOK,
		OutstandingSurvivedStop: bytes.Equal(beforeShutdown, afterShutdown),
		PostShutdownCode: func() string {
			if postRC == 0 {
				return ""
			}
			return postCode
		}(),
	}, nil
}

func callAndFree(plugin *C.cliproxy_plugin_api, method string, request []byte, duplicateFree bool) ([]byte, int, error) {
	response, rc, errCall := callRetained(plugin, method, request)
	if errCall != nil {
		return nil, rc, errCall
	}
	raw, errCopy := copyResponse(response)
	if response.ptr != nil {
		C.free_plugin(plugin.free_buffer, response.ptr, response.len)
		if duplicateFree {
			C.free_plugin(plugin.free_buffer, response.ptr, response.len)
		}
	}
	return raw, rc, errCopy
}

func callRetained(plugin *C.cliproxy_plugin_api, method string, request []byte) (C.cliproxy_buffer, int, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var requestPtr unsafe.Pointer
	if len(request) > 0 {
		requestPtr = C.CBytes(request)
		defer C.free(requestPtr)
	}
	var response C.cliproxy_buffer
	rc := C.call_plugin(plugin.call, cMethod, (*C.uint8_t)(requestPtr), C.size_t(len(request)), &response)
	if response.len > C.size_t(abi.MaxResponseBytes) {
		if response.ptr != nil {
			C.free_plugin(plugin.free_buffer, response.ptr, response.len)
		}
		return C.cliproxy_buffer{}, int(rc), fmt.Errorf("response exceeds limit")
	}
	return response, int(rc), nil
}

func copyResponse(response C.cliproxy_buffer) ([]byte, error) {
	if response.ptr == nil || response.len == 0 {
		return nil, nil
	}
	if response.len > C.size_t(abi.MaxResponseBytes) {
		return nil, fmt.Errorf("response exceeds limit")
	}
	return C.GoBytes(response.ptr, C.int(response.len)), nil
}

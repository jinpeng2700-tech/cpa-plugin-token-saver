//go:build cgo

package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

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

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static size_t cliproxy_bounded_strlen(const char* value, size_t limit) {
	if (value == NULL) {
		return 0;
	}
	size_t length = 0;
	while (length < limit && value[length] != '\0') {
		length++;
	}
	return length;
}
*/
import "C"

import (
	"unsafe"

	"github.com/router-for-me/cpa-plugin-token-saver/internal/abi"
)

var (
	pluginRuntime   = abi.NewRuntime()
	responseBuffers = abi.NewBufferPool()
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil || pluginRuntime.Stopped() {
		return 1
	}
	if host != nil && uint32(host.abi_version) != abi.ABIVersion {
		return 1
	}
	plugin.abi_version = C.uint32_t(abi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	if method == nil {
		writeResponse(response, abi.Failure("invalid_method", "method is required"))
		return 1
	}
	methodLength := uintptr(C.cliproxy_bounded_strlen(method, C.size_t(abi.MaxMethodBytes+1)))
	if methodLength > abi.MaxMethodBytes {
		writeResponse(response, abi.Failure("invalid_method", "method exceeds 256-byte limit"))
		return 1
	}
	requestBytes, errCopy := abi.CopyRequest(unsafe.Pointer(request), uintptr(requestLen))
	if errCopy != nil {
		writeResponse(response, abi.Failure("invalid_request", errCopy.Error()))
		return 1
	}
	raw, status := pluginRuntime.Call(C.GoStringN(method, C.int(methodLength)), requestBytes)
	if !writeResponse(response, raw) {
		return 1
	}
	return C.int(status)
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	responseBuffers.Free(ptr, uintptr(length), func(owned unsafe.Pointer) {
		C.free(owned)
	})
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	pluginRuntime.Shutdown()
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) bool {
	if response == nil {
		return false
	}
	ptr, length, errAllocate := responseBuffers.Allocate(raw, func(data []byte) unsafe.Pointer {
		return C.CBytes(data)
	})
	if errAllocate != nil {
		return false
	}
	response.ptr = ptr
	response.len = C.size_t(length)
	return true
}

package abi

import (
	"bytes"
	"fmt"
	"sync"
	"unsafe"
)

const (
	MaxMethodBytes   = 256
	MaxRequestBytes  = 16 << 20
	MaxResponseBytes = 16 << 20
)

// AllocateFunc allocates native memory owned by the plugin until FreeFunc runs.
type AllocateFunc func([]byte) unsafe.Pointer

// FreeFunc releases one native allocation.
type FreeFunc func(unsafe.Pointer)

// BufferPool tracks plugin-allocated response pointers. Shutdown does not clear
// this pool: the host remains the owner of returned buffers and may free them
// after plugin shutdown.
type BufferPool struct {
	mu          sync.Mutex
	allocations map[unsafe.Pointer]uintptr
}

func NewBufferPool() *BufferPool {
	return &BufferPool{allocations: make(map[unsafe.Pointer]uintptr)}
}

// Allocate creates one tracked plugin-owned response buffer.
func (p *BufferPool) Allocate(raw []byte, allocate AllocateFunc) (unsafe.Pointer, uintptr, error) {
	if len(raw) == 0 {
		return nil, 0, nil
	}
	if len(raw) > MaxResponseBytes {
		return nil, 0, fmt.Errorf("response exceeds %d-byte limit", MaxResponseBytes)
	}
	if p == nil || allocate == nil {
		return nil, 0, fmt.Errorf("response allocator is unavailable")
	}
	ptr := allocate(raw)
	if ptr == nil {
		return nil, 0, fmt.Errorf("response allocation failed")
	}
	length := uintptr(len(raw))
	p.mu.Lock()
	if p.allocations == nil {
		p.allocations = make(map[unsafe.Pointer]uintptr)
	}
	if _, exists := p.allocations[ptr]; exists {
		p.mu.Unlock()
		return nil, 0, fmt.Errorf("response allocator returned a live pointer")
	}
	p.allocations[ptr] = length
	p.mu.Unlock()
	return ptr, length, nil
}

// Free releases a tracked pointer exactly once. Unknown and duplicate pointers
// are ignored. The reported length is informational because pointer identity is
// the ownership token and a stale length must not cause a leak or second free.
func (p *BufferPool) Free(ptr unsafe.Pointer, reportedLength uintptr, free FreeFunc) bool {
	_ = reportedLength
	if p == nil || ptr == nil || free == nil {
		return false
	}
	p.mu.Lock()
	if _, exists := p.allocations[ptr]; !exists {
		p.mu.Unlock()
		return false
	}
	delete(p.allocations, ptr)
	p.mu.Unlock()
	free(ptr)
	return true
}

func (p *BufferPool) Outstanding() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.allocations)
}

// CopyRequest validates and copies host-owned request bytes before dispatch.
// Callers intentionally invoke this outside Runtime.Call's panic recovery so
// invalid native memory cannot be mistaken for a recoverable plugin error.
func CopyRequest(ptr unsafe.Pointer, length uintptr) ([]byte, error) {
	if length == 0 {
		return nil, nil
	}
	if length > MaxRequestBytes {
		return nil, fmt.Errorf("request exceeds %d-byte limit", MaxRequestBytes)
	}
	if ptr == nil {
		return nil, fmt.Errorf("request pointer is nil for non-empty request")
	}
	return bytes.Clone(unsafe.Slice((*byte)(ptr), int(length))), nil
}

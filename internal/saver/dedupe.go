package saver

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

const (
	dedupeTTL          = 5 * time.Second
	dedupeMaxEntries   = 64
	dedupeMaxBytes     = 8 << 20
	dedupeMaxItemBytes = 1 << 20
)

var errDedupeWorkPanic = errors.New("dedupe work panicked")

type dedupeCache struct {
	mu         sync.Mutex
	now        func() time.Time
	entries    map[[sha256.Size]byte]*dedupeEntry
	sequence   uint64
	totalBytes int
}

type dedupeEntry struct {
	done      chan struct{}
	value     []byte
	err       error
	expiresAt time.Time
	sequence  uint64
	complete  bool
}

func newDedupe(now func() time.Time) *dedupeCache {
	if now == nil {
		now = time.Now
	}
	return &dedupeCache{now: now, entries: make(map[[sha256.Size]byte]*dedupeEntry)}
}

// Do coalesces equal in-flight work and retains bounded completed results.
// Returned byte slices never alias the cache or another caller.
func (cache *dedupeCache) Do(key [sha256.Size]byte, work func() ([]byte, error)) ([]byte, bool, error) {
	if cache == nil || work == nil {
		if work == nil {
			return nil, false, nil
		}
		value, err := runDedupeWork(work)
		return cloneBytes(value), false, err
	}

	now := cache.now()
	cache.mu.Lock()
	cache.expireLocked(now)
	if existing := cache.entries[key]; existing != nil {
		done := existing.done
		cache.mu.Unlock()
		<-done
		return cloneBytes(existing.value), true, existing.err
	}
	cache.makeRoomLocked()
	if len(cache.entries) >= dedupeMaxEntries {
		cache.mu.Unlock()
		value, err := runDedupeWork(work)
		return cloneBytes(value), false, err
	}
	cache.sequence++
	entry := &dedupeEntry{done: make(chan struct{}), sequence: cache.sequence}
	cache.entries[key] = entry
	cache.mu.Unlock()

	value, err := runDedupeWork(work)
	value = cloneBytes(value)

	cache.mu.Lock()
	entry.value = value
	entry.err = err
	entry.complete = true
	entry.expiresAt = cache.now().Add(dedupeTTL)
	if err != nil || len(value) > dedupeMaxItemBytes {
		delete(cache.entries, key)
	} else {
		cache.totalBytes += len(value)
		cache.enforceByteBudgetLocked()
	}
	close(entry.done)
	cache.mu.Unlock()
	return cloneBytes(value), false, err
}

func runDedupeWork(work func() ([]byte, error)) (value []byte, err error) {
	defer func() {
		if recover() != nil {
			value = nil
			err = errDedupeWorkPanic
		}
	}()
	return work()
}

func (cache *dedupeCache) expireLocked(now time.Time) {
	for key, entry := range cache.entries {
		if entry.complete && !now.Before(entry.expiresAt) {
			cache.removeLocked(key, entry)
		}
	}
}

func (cache *dedupeCache) makeRoomLocked() {
	for len(cache.entries) >= dedupeMaxEntries {
		key, entry, ok := cache.oldestCompletedLocked()
		if !ok {
			return
		}
		cache.removeLocked(key, entry)
	}
}

func (cache *dedupeCache) enforceByteBudgetLocked() {
	for cache.totalBytes > dedupeMaxBytes {
		key, entry, ok := cache.oldestCompletedLocked()
		if !ok {
			return
		}
		cache.removeLocked(key, entry)
	}
}

func (cache *dedupeCache) oldestCompletedLocked() ([sha256.Size]byte, *dedupeEntry, bool) {
	var selectedKey [sha256.Size]byte
	var selected *dedupeEntry
	for key, entry := range cache.entries {
		if !entry.complete || selected != nil && entry.sequence >= selected.sequence {
			continue
		}
		selectedKey = key
		selected = entry
	}
	return selectedKey, selected, selected != nil
}

func (cache *dedupeCache) removeLocked(key [sha256.Size]byte, entry *dedupeEntry) {
	if current := cache.entries[key]; current != entry {
		return
	}
	delete(cache.entries, key)
	if entry.complete {
		cache.totalBytes -= len(entry.value)
		if cache.totalBytes < 0 {
			cache.totalBytes = 0
		}
	}
}

func dedupeKey(generation uint64, request Request) [sha256.Size]byte {
	hash := sha256.New()
	var scalar [8]byte
	binary.BigEndian.PutUint64(scalar[:], generation)
	_, _ = hash.Write(scalar[:])
	writeHashField(hash, []byte(request.FromFormat))
	writeHashField(hash, []byte(request.ToFormat))
	writeHashField(hash, []byte(request.Model))
	if request.Stream {
		_, _ = hash.Write([]byte{1})
	} else {
		_, _ = hash.Write([]byte{0})
	}
	writeHashField(hash, request.Body)
	var key [sha256.Size]byte
	copy(key[:], hash.Sum(nil))
	return key
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeHashField(writer hashWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

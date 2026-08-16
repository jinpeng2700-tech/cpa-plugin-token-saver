package saver

import (
	"bytes"
	"crypto/sha256"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDedupeCoalescesConcurrentWorkAndExpiresWithFakeClock(t *testing.T) {
	clock := &dedupeClock{now: time.Unix(100, 0)}
	cache := newDedupe(clock.Now)
	key := dedupeKey(7, Request{
		FromFormat: "openai", ToFormat: "openai", Model: "model-a", Stream: true,
		Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	})

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	work := func() ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []byte("intermediate"), nil
	}

	var first, second []byte
	var firstErr, secondErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		first, _, firstErr = cache.Do(key, work)
	}()
	<-started
	go func() {
		defer wait.Done()
		second, _, secondErr = cache.Do(key, work)
	}()
	close(release)
	wait.Wait()

	if firstErr != nil || secondErr != nil {
		t.Fatalf("Do errors = %v / %v", firstErr, secondErr)
	}
	if calls.Load() != 1 || !bytes.Equal(first, second) {
		t.Fatalf("calls/results = %d / %q / %q", calls.Load(), first, second)
	}

	clock.Advance(5 * time.Second)
	_, _, err := cache.Do(key, func() ([]byte, error) {
		calls.Add(1)
		return []byte("fresh"), nil
	})
	if err != nil || calls.Load() != 2 {
		t.Fatalf("expired call = %d, error = %v", calls.Load(), err)
	}
}

func TestDedupeEnforcesEntryItemAndByteBudgetsDeterministically(t *testing.T) {
	clock := &dedupeClock{now: time.Unix(200, 0)}
	cache := newDedupe(clock.Now)
	for index := 0; index < dedupeMaxEntries+5; index++ {
		key := testDedupeKey(index)
		if _, _, err := cache.Do(key, func() ([]byte, error) { return []byte{byte(index)}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	entryCount := len(cache.entries)
	cache.mu.Unlock()
	if entryCount != dedupeMaxEntries {
		t.Fatalf("entries = %d, want %d", entryCount, dedupeMaxEntries)
	}
	var oldestCalls atomic.Int32
	_, _, err := cache.Do(testDedupeKey(0), func() ([]byte, error) {
		oldestCalls.Add(1)
		return []byte("recomputed"), nil
	})
	if err != nil || oldestCalls.Load() != 1 {
		t.Fatalf("oldest entry was not evicted: calls=%d err=%v", oldestCalls.Load(), err)
	}

	byteCache := newDedupe(clock.Now)
	item := bytes.Repeat([]byte{'x'}, dedupeMaxItemBytes)
	for index := 0; index < 9; index++ {
		if _, _, err := byteCache.Do(testDedupeKey(index), func() ([]byte, error) { return item, nil }); err != nil {
			t.Fatal(err)
		}
	}
	byteCache.mu.Lock()
	totalBytes := byteCache.totalBytes
	retained := len(byteCache.entries)
	byteCache.mu.Unlock()
	if totalBytes > dedupeMaxBytes || retained != dedupeMaxBytes/dedupeMaxItemBytes {
		t.Fatalf("byte budget = %d bytes across %d entries", totalBytes, retained)
	}

	largeCache := newDedupe(clock.Now)
	large := bytes.Repeat([]byte{'y'}, dedupeMaxItemBytes+1)
	var largeCalls atomic.Int32
	for range 2 {
		_, _, errLarge := largeCache.Do(testDedupeKey(99), func() ([]byte, error) {
			largeCalls.Add(1)
			return large, nil
		})
		if errLarge != nil {
			t.Fatal(errLarge)
		}
	}
	if largeCalls.Load() != 2 {
		t.Fatalf("oversized item calls = %d, want 2", largeCalls.Load())
	}
}

func TestDedupeKeyIncludesEveryRequestDimension(t *testing.T) {
	base := Request{FromFormat: "openai", ToFormat: "codex", Model: "m", Stream: true, Body: []byte("body")}
	keys := map[[sha256.Size]byte]bool{dedupeKey(1, base): true}
	variants := []struct {
		generation uint64
		request    Request
	}{
		{2, base},
		{1, Request{FromFormat: "claude", ToFormat: base.ToFormat, Model: base.Model, Stream: base.Stream, Body: base.Body}},
		{1, Request{FromFormat: base.FromFormat, ToFormat: "openai", Model: base.Model, Stream: base.Stream, Body: base.Body}},
		{1, Request{FromFormat: base.FromFormat, ToFormat: base.ToFormat, Model: "other", Stream: base.Stream, Body: base.Body}},
		{1, Request{FromFormat: base.FromFormat, ToFormat: base.ToFormat, Model: base.Model, Stream: false, Body: base.Body}},
		{1, Request{FromFormat: base.FromFormat, ToFormat: base.ToFormat, Model: base.Model, Stream: base.Stream, Body: []byte("other")}},
	}
	for index, variant := range variants {
		key := dedupeKey(variant.generation, variant.request)
		if keys[key] {
			t.Fatalf("variant %d reused a key", index)
		}
		keys[key] = true
	}
}

func TestDedupePanicDoesNotPoisonKey(t *testing.T) {
	cache := newDedupe(time.Now)
	key := testDedupeKey(777)
	if _, _, err := cache.Do(key, func() ([]byte, error) {
		panic("boom")
	}); err == nil {
		t.Fatal("panic was not converted to a dedupe error")
	}

	value, hit, err := cache.Do(key, func() ([]byte, error) {
		return []byte("fresh"), nil
	})
	if err != nil || hit || string(value) != "fresh" {
		t.Fatalf("key remained poisoned: value=%q hit=%v err=%v", value, hit, err)
	}
}

func testDedupeKey(index int) [sha256.Size]byte {
	return sha256.Sum256([]byte{byte(index), byte(index >> 8)})
}

type dedupeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *dedupeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *dedupeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

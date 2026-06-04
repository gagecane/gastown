package lock

import (
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlockSemaphore_AcquiresUpToN(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("advisory flock is a no-op on Windows")
	}
	dir := filepath.Join(t.TempDir(), "slots")
	sem := NewFlockSemaphore(dir, 2)

	r1, err := sem.Acquire(time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	r2, err := sem.Acquire(time.Second)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	// Third acquire must block until a slot frees, then time out.
	if _, err := sem.Acquire(100 * time.Millisecond); err == nil {
		t.Fatal("third acquire should time out while both slots held")
	}

	// Release one slot; a new acquire should now succeed.
	r1()
	r3, err := sem.Acquire(time.Second)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	r3()
	r2()
}

func TestFlockSemaphore_ClampsToOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("advisory flock is a no-op on Windows")
	}
	dir := filepath.Join(t.TempDir(), "slots")
	sem := NewFlockSemaphore(dir, 0) // clamps to 1

	r1, err := sem.Acquire(time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := sem.Acquire(100 * time.Millisecond); err == nil {
		t.Fatal("second acquire should time out with a single slot")
	}
	r1()
}

func TestFlockSemaphore_BoundsConcurrency(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("advisory flock is a no-op on Windows")
	}
	// flock is per-process advisory: two acquires in the SAME process on the
	// same fd path don't block each other the way separate processes do. So we
	// model "separate holders" by giving each goroutine its own semaphore
	// instance over the same dir — each opens its own fd, so flock contends.
	origInterval := semaphoreRetryInterval
	semaphoreRetryInterval = 5 * time.Millisecond
	defer func() { semaphoreRetryInterval = origInterval }()

	dir := filepath.Join(t.TempDir(), "slots")
	const n = 2
	const workers = 8

	var live int32
	var maxLive int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem := NewFlockSemaphore(dir, n)
			release, err := sem.Acquire(5 * time.Second)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			cur := atomic.AddInt32(&live, 1)
			for {
				m := atomic.LoadInt32(&maxLive)
				if cur <= m || atomic.CompareAndSwapInt32(&maxLive, m, cur) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&live, -1)
			release()
		}()
	}
	wg.Wait()

	if maxLive > n {
		t.Fatalf("max concurrent holders = %d, want <= %d", maxLive, n)
	}
}

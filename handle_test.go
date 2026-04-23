// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/plugins/drivers"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestHandle() *taskHandle {
	ctx, cancel := context.WithCancel(context.Background())
	return &taskHandle{
		containerName: "test-container",
		containerPath: "/usr/local/bin/container",
		logger:        hclog.NewNullLogger(),
		taskConfig:    &drivers.TaskConfig{},
		procState:     drivers.TaskStateRunning,
		startedAt:     time.Now(),
		doneCh:        make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// ── taskStore ─────────────────────────────────────────────────────────────────

func TestTaskStore_SetAndGet(t *testing.T) {
	ts := newTaskStore()
	h := &taskHandle{containerName: "test"}

	if _, ok := ts.Get("id1"); ok {
		t.Fatal("expected empty store")
	}

	ts.Set("id1", h)
	got, ok := ts.Get("id1")
	if !ok {
		t.Fatal("expected to find id1")
	}
	if got.containerName != "test" {
		t.Errorf("containerName = %q; want test", got.containerName)
	}
}

func TestTaskStore_Delete(t *testing.T) {
	ts := newTaskStore()
	ts.Set("id1", &taskHandle{containerName: "c"})
	ts.Delete("id1")
	if _, ok := ts.Get("id1"); ok {
		t.Error("expected id1 to be deleted")
	}
}

func TestTaskStore_Range(t *testing.T) {
	ts := newTaskStore()
	ts.Set("a", &taskHandle{containerName: "ca"})
	ts.Set("b", &taskHandle{containerName: "cb"})

	seen := make(map[string]bool)
	ts.Range(func(id string, h *taskHandle) {
		seen[id] = true
	})
	if !seen["a"] || !seen["b"] || len(seen) != 2 {
		t.Errorf("Range did not visit all entries: %v", seen)
	}
}

func TestTaskStore_ConcurrentAccess(t *testing.T) {
	ts := newTaskStore()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("id-%d", n)
			ts.Set(id, &taskHandle{containerName: id})
			ts.Get(id)
			ts.Delete(id)
		}(i)
	}
	wg.Wait()
}

// ── taskHandle state ──────────────────────────────────────────────────────────

func TestTaskHandle_IsRunning_InitiallyTrue(t *testing.T) {
	h := newTestHandle()
	if !h.IsRunning() {
		t.Error("expected IsRunning() = true for a new handle")
	}
}

func TestTaskHandle_SetExitResult(t *testing.T) {
	h := newTestHandle()
	result := &drivers.ExitResult{ExitCode: 42}
	h.setExitResult(result)

	if h.IsRunning() {
		t.Error("expected IsRunning() = false after setExitResult")
	}
	got := h.ExitResult()
	if got == nil {
		t.Fatal("ExitResult() should not be nil after setExitResult")
	}
	if got.ExitCode != 42 {
		t.Errorf("ExitCode = %v; want 42", got.ExitCode)
	}
	if h.completedAt.IsZero() {
		t.Error("completedAt should be set after setExitResult")
	}
}

func TestTaskHandle_ExitResult_NilBeforeExit(t *testing.T) {
	h := newTestHandle()
	if got := h.ExitResult(); got != nil {
		t.Errorf("ExitResult() before exit = %+v; want nil", got)
	}
}

// ── runWaitLoop ───────────────────────────────────────────────────────────────

// setFastPollInterval reduces taskPollInterval for the duration of a test,
// restoring it afterwards to avoid affecting other tests.
func setFastPollInterval(t *testing.T) {
	t.Helper()
	orig := taskPollInterval
	taskPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { taskPollInterval = orig })
}

func TestRunWaitLoop_ContainerExitsNormally(t *testing.T) {
	setFastPollInterval(t)
	h := newTestHandle()

	callCount := 0
	inspectFn := func(name string) (*inspectData, error) {
		callCount++
		if callCount < 3 {
			return &inspectData{State: inspectState{Status: "running"}}, nil
		}
		return &inspectData{State: inspectState{Status: "stopped", ExitCode: 0}}, nil
	}

	go h.runWaitLoop(inspectFn)

	select {
	case <-h.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("runWaitLoop did not complete in time")
	}

	if h.IsRunning() {
		t.Error("expected IsRunning() = false after loop exits")
	}
	if h.ExitResult().ExitCode != 0 {
		t.Errorf("ExitCode = %v; want 0", h.ExitResult().ExitCode)
	}
}

func TestRunWaitLoop_ContainerExitsWithNonZeroCode(t *testing.T) {
	setFastPollInterval(t)
	h := newTestHandle()

	inspectFn := func(name string) (*inspectData, error) {
		return &inspectData{State: inspectState{Status: "exited", ExitCode: 1}}, nil
	}

	go h.runWaitLoop(inspectFn)

	select {
	case <-h.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("runWaitLoop did not complete in time")
	}

	if h.ExitResult().ExitCode != 1 {
		t.Errorf("ExitCode = %v; want 1", h.ExitResult().ExitCode)
	}
}

func TestRunWaitLoop_ContextCancelled(t *testing.T) {
	setFastPollInterval(t)
	h := newTestHandle()

	// inspectFn always returns "running" — exit must come from cancellation.
	inspectFn := func(name string) (*inspectData, error) {
		return &inspectData{State: inspectState{Status: "running"}}, nil
	}

	go h.runWaitLoop(inspectFn)
	h.cancel() // trigger cancellation immediately

	select {
	case <-h.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("runWaitLoop did not exit after context cancellation")
	}

	result := h.ExitResult()
	if result == nil {
		t.Fatal("expected exit result after cancellation, got nil")
	}
	if result.ExitCode != 128 {
		t.Errorf("ExitCode = %v; want 128", result.ExitCode)
	}
}

func TestRunWaitLoop_InspectError(t *testing.T) {
	setFastPollInterval(t)
	h := newTestHandle()

	inspectFn := func(name string) (*inspectData, error) {
		return nil, fmt.Errorf("container %q not found", name)
	}

	go h.runWaitLoop(inspectFn)

	select {
	case <-h.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("runWaitLoop did not exit after inspect error")
	}

	result := h.ExitResult()
	if result == nil {
		t.Fatal("expected exit result after inspect error, got nil")
	}
	if result.ExitCode != 128 {
		t.Errorf("ExitCode = %v; want 128", result.ExitCode)
	}
	if result.Err == nil {
		t.Error("expected Err to be set after inspect error")
	}
}

func TestRunWaitLoop_StartingStatus_KeepsPolling(t *testing.T) {
	setFastPollInterval(t)
	h := newTestHandle()

	callCount := 0
	inspectFn := func(name string) (*inspectData, error) {
		callCount++
		if callCount < 3 {
			return &inspectData{State: inspectState{Status: "starting"}}, nil
		}
		return &inspectData{State: inspectState{Status: "running", ExitCode: 0}}, nil
	}

	go h.runWaitLoop(inspectFn)

	// "starting" and "running" are both alive; the loop should keep going.
	// Cancel after a short window to prove it did NOT exit on "starting".
	time.Sleep(50 * time.Millisecond)
	select {
	case <-h.doneCh:
		t.Error("runWaitLoop should NOT have exited: container was starting/running")
	default:
	}

	h.cancel()
	select {
	case <-h.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("runWaitLoop did not exit after explicit cancel")
	}
}

func TestRunWaitLoop_DoneCh_ClosedExactlyOnce(t *testing.T) {
	setFastPollInterval(t)
	h := newTestHandle()

	inspectFn := func(name string) (*inspectData, error) {
		return &inspectData{State: inspectState{Status: "stopped"}}, nil
	}

	go h.runWaitLoop(inspectFn)

	// Receiving from a closed channel returns immediately; receiving twice
	// from a channel closed only once also returns immediately (zero value).
	// If doneCh is closed more than once, the second close panics.
	// The defer close(h.doneCh) in runWaitLoop prevents that.
	<-h.doneCh
}

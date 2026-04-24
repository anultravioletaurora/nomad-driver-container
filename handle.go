// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/client/lib/fifo"
	cstructs "github.com/hashicorp/nomad/client/structs"
	"github.com/hashicorp/nomad/plugins/drivers"
)

// taskHandle is the in-memory representation of a running container task.
// It is created in StartTask and removed in DestroyTask.
type taskHandle struct {
	// containerName is the deterministic name given to the container.
	containerName string

	// containerPath is the path to the container CLI binary.
	containerPath string

	// logger is scoped to this task handle.
	logger hclog.Logger

	// stateLock protects procState, exitResult and completedAt.
	stateLock sync.RWMutex

	// taskConfig is the Nomad task configuration.
	taskConfig *drivers.TaskConfig

	// procState is the current lifecycle state reported to Nomad.
	procState drivers.TaskState

	// startedAt is when the container was started.
	startedAt time.Time

	// completedAt is when the container exited (zero while running).
	completedAt time.Time

	// exitResult is populated once the container has exited.
	exitResult *drivers.ExitResult

	// doneCh is closed once the container exits (or the context is cancelled).
	doneCh chan struct{}

	// ctx is cancelled when StopTask / DestroyTask is called.
	ctx    context.Context
	cancel context.CancelFunc
}

// ExitResult returns the container's exit result.  Blocks until the container
// has exited if it is still running; use the doneCh to avoid blocking.
func (h *taskHandle) ExitResult() *drivers.ExitResult {
	h.stateLock.RLock()
	defer h.stateLock.RUnlock()
	return h.exitResult
}

// IsRunning returns true when the task is in the running state.
func (h *taskHandle) IsRunning() bool {
	h.stateLock.RLock()
	defer h.stateLock.RUnlock()
	return h.procState == drivers.TaskStateRunning
}

// setExitResult records the exit result and marks the task as exited.
func (h *taskHandle) setExitResult(result *drivers.ExitResult) {
	h.stateLock.Lock()
	defer h.stateLock.Unlock()
	h.exitResult = result
	h.completedAt = time.Now()
	h.procState = drivers.TaskStateExited
}

// taskPollInterval is how often runWaitLoop checks the container status.
// It is a variable (rather than a constant) so tests can reduce it.
var taskPollInterval = 2 * time.Second

// runWaitLoop polls `container inspect` until the container exits, then
// records the exit result and closes doneCh.  It is started as a goroutine
// by StartTask.
func (h *taskHandle) runWaitLoop(inspectFn func(name string) (*inspectData, error)) {
	defer close(h.doneCh)

	pollInterval := taskPollInterval

	for {
		select {
		case <-h.ctx.Done():
			// Context was cancelled (task stopped/destroyed). Record the
			// cancellation as a signal exit if no result was set yet.
			h.stateLock.Lock()
			if h.exitResult == nil {
				h.exitResult = &drivers.ExitResult{
					ExitCode: 128,
					Signal:   int(signum("SIGKILL")),
				}
				h.completedAt = time.Now()
				h.procState = drivers.TaskStateExited
			}
			h.stateLock.Unlock()
			return

		case <-time.After(pollInterval):
		}

		info, err := inspectFn(h.containerName)
		if err != nil {
			// The container may have already been removed; treat as exited.
			h.setExitResult(&drivers.ExitResult{ExitCode: 128, Err: err})
			return
		}

		switch info.Status {
		case "running", "starting":
			// Still alive – keep polling.
			continue
		default:
			// Any other status (stopped, exited, dead, etc.) means done.
			h.setExitResult(&drivers.ExitResult{
					ExitCode: info.ExitCode,
			})
			return
		}
	}
}

// streamLogs starts `container logs --follow <name>` and pipes its output to
// the Nomad task stdout FIFO.  Because Apple's `container logs` combines
// stdout and stderr into a single stream, everything is forwarded to the
// Nomad stdout FIFO.  stderr FIFO is left empty.
//
// This function blocks until the log stream ends or the context is cancelled,
// and should be run as a goroutine.
func (h *taskHandle) streamLogs(stdoutPath string) {
	stdout, err := fifo.OpenWriter(stdoutPath)
	if err != nil {
		h.logger.Error("failed to open stdout FIFO for log collection",
			"error", err, "path", stdoutPath)
		return
	}
	defer stdout.Close()

	cmd := exec.CommandContext(h.ctx, h.containerPath,
		"logs", "--follow", h.containerName)
	cmd.Stdout = stdout
	cmd.Stderr = stdout // combined stream – see NOTE in README

	if err := cmd.Run(); err != nil {
		// Errors here are expected when the container exits or the context is
		// cancelled; log at debug level only.
		h.logger.Debug("log stream ended", "error", err)
	}
}

// collectStats sends resource usage samples on ch at the given interval until
// ctx is cancelled.  It runs `container stats --no-stream --format json` on
// each tick and parses the JSON output.
func (h *taskHandle) collectStats(
	ctx context.Context,
	ch chan<- *cstructs.TaskResourceUsage,
	interval time.Duration,
) {
	defer close(ch)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		usage, err := h.sampleStats()
		if err != nil {
			h.logger.Debug("stats collection failed", "error", err)
			continue
		}
		select {
		case ch <- usage:
		case <-ctx.Done():
			return
		}
	}
}

// sampleStats runs `container stats --no-stream --format json` once and
// returns the parsed resource usage.
func (h *taskHandle) sampleStats() (*cstructs.TaskResourceUsage, error) {
	out, err := exec.CommandContext(h.ctx,
		h.containerPath,
		"stats", "--no-stream", "--format", "json",
		h.containerName,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("container stats: %w", err)
	}

	stats, err := parseStats(out, h.containerName)
	if err != nil {
		return nil, err
	}

	return statsToTaskResourceUsage(stats), nil
}

// taskStore is a thread-safe map of taskID → taskHandle.
type taskStore struct {
	mu    sync.RWMutex
	store map[string]*taskHandle
}

func newTaskStore() *taskStore {
	return &taskStore{store: make(map[string]*taskHandle)}
}

func (ts *taskStore) Set(id string, h *taskHandle) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.store[id] = h
}

func (ts *taskStore) Get(id string) (*taskHandle, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	h, ok := ts.store[id]
	return h, ok
}

func (ts *taskStore) Delete(id string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.store, id)
}

func (ts *taskStore) Range(fn func(id string, h *taskHandle)) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	for id, h := range ts.store {
		fn(id, h)
	}
}

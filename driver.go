// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	cstructs "github.com/hashicorp/nomad/client/structs"
	"github.com/hashicorp/nomad/drivers/shared/eventer"
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
	"github.com/hashicorp/nomad/plugins/drivers/fsisolation"
	"github.com/hashicorp/nomad/plugins/shared/hclspec"

	drv_version "github.com/hashicorp/nomad-driver-container/version"
)

// pluginVersion is the semantic version of this driver plugin.
var pluginVersion = "v" + drv_version.Version

const (
	// PluginName is the name of the driver plugin used in Nomad job specs.
	PluginName = "container"

	// defaultContainerPath is the default location of the Apple container CLI.
	defaultContainerPath = "/usr/local/bin/container"

	// fingerprintPeriod is how often the driver re-checks the container
	// system health.
	fingerprintPeriod = 30 * time.Second

	// defaultImagePullTimeout is the default time limit for image pulls.
	defaultImagePullTimeout = 5 * time.Minute
)

// pluginInfo is the static metadata returned by PluginInfo().
var pluginInfo = &base.PluginInfoResponse{
	Type:              base.PluginTypeDriver,
	PluginApiVersions: []string{drivers.ApiVersion010},
	PluginVersion:     pluginVersion,
	Name:              PluginName,
}

// capabilities advertises what the driver supports to the Nomad client.
var capabilities = &drivers.Capabilities{
	SendSignals: true,
	Exec:        true,
	FSIsolation: fsisolation.Image,
	NetIsolationModes: []drivers.NetIsolationMode{
		drivers.NetIsolationModeHost,
		drivers.NetIsolationModeGroup,
		drivers.NetIsolationModeTask,
	},
	MustInitiateNetwork: false,
	MountConfigs:        drivers.MountConfigSupportAll,
}

// Driver is the Apple Container Nomad task driver.
type Driver struct {
	// config is the plugin configuration set by SetConfig.
	config *PluginConfig

	// nomadConfig is the Nomad agent's own configuration, set by SetConfig.
	nomadConfig *base.AgentConfig

	// tasks is the in-memory store of running task handles.
	tasks *taskStore

	// eventer is used to push driver events to Nomad.
	eventer *eventer.Eventer

	// logger is the scoped logger for this driver instance.
	logger hclog.Logger

	// ctx and signalShutdown are used to stop the driver cleanly.
	ctx             context.Context
	signalShutdown  context.CancelFunc
}

// NewDriver creates a new instance of the Container driver.
func NewDriver(logger hclog.Logger) *Driver {
	ctx, cancel := context.WithCancel(context.Background())
	return &Driver{
		config:         &PluginConfig{},
		tasks:          newTaskStore(),
		eventer:        eventer.NewEventer(ctx, logger),
		logger:         logger.Named(PluginName),
		ctx:            ctx,
		signalShutdown: cancel,
	}
}

// ────────────────────────────────────────
// BasePlugin interface
// ────────────────────────────────────────

func (d *Driver) PluginInfo() (*base.PluginInfoResponse, error) {
	return pluginInfo, nil
}

func (d *Driver) ConfigSchema() (*hclspec.Spec, error) {
	return configSpec, nil
}

func (d *Driver) SetConfig(cfg *base.Config) error {
	var config PluginConfig
	if len(cfg.PluginConfig) != 0 {
		if err := base.MsgPackDecode(cfg.PluginConfig, &config); err != nil {
			return fmt.Errorf("failed to decode plugin config: %w", err)
		}
	}

	// Apply defaults for zero values.
	if config.ContainerPath == "" {
		config.ContainerPath = defaultContainerPath
	}
	if config.ImagePullTimeout == "" {
		config.ImagePullTimeout = "5m"
	}
	if !config.GC.Container {
		// Default to cleaning up containers on task exit.
		config.GC.Container = true
	}
	if !config.Volumes.Enabled {
		config.Volumes.Enabled = true
	}

	d.config = &config
	if cfg.AgentConfig != nil {
		d.nomadConfig = cfg.AgentConfig
	}

	return nil
}

// ────────────────────────────────────────
// DriverPlugin interface
// ────────────────────────────────────────

func (d *Driver) TaskConfigSchema() (*hclspec.Spec, error) {
	return taskConfigSpec, nil
}

func (d *Driver) Capabilities() (*drivers.Capabilities, error) {
	return capabilities, nil
}

// Fingerprint reports driver health to the Nomad client on a recurring basis.
func (d *Driver) Fingerprint(ctx context.Context) (<-chan *drivers.Fingerprint, error) {
	ch := make(chan *drivers.Fingerprint)
	go d.fingerprintLoop(ctx, ch)
	return ch, nil
}

func (d *Driver) fingerprintLoop(ctx context.Context, ch chan<- *drivers.Fingerprint) {
	defer close(ch)
	ticker := time.NewTimer(0) // fire immediately
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ch <- d.buildFingerprint()
			ticker.Reset(fingerprintPeriod)
		}
	}
}

func (d *Driver) buildFingerprint() *drivers.Fingerprint {
	binPath := d.containerPath()

	// 1. Check the binary exists and is executable.
	if _, err := os.Stat(binPath); err != nil {
		return &drivers.Fingerprint{
			Health:            drivers.HealthStateUndetected,
			HealthDescription: fmt.Sprintf("container binary not found at %s", binPath),
		}
	}

	// 2. Check the system service is running.
	if err := exec.Command(binPath, "system", "status").Run(); err != nil {
		return &drivers.Fingerprint{
			Health:            drivers.HealthStateUnhealthy,
			HealthDescription: "container system service is not running; run: container system start",
		}
	}

	// 3. Optionally collect the CLI version for the health description.
	version := "unknown"
	if out, err := exec.Command(binPath, "system", "version", "--format", "json").Output(); err == nil {
		var ver versionOutput
		if json.Unmarshal(out, &ver) == nil && ver.Version != "" {
			version = ver.Version
		}
	}

	return &drivers.Fingerprint{
		Health:            drivers.HealthStateHealthy,
		HealthDescription: fmt.Sprintf("container %s healthy", version),
	}
}

// StartTask validates a task configuration and starts the container.
func (d *Driver) StartTask(cfg *drivers.TaskConfig) (*drivers.TaskHandle, *drivers.DriverNetwork, error) {
	if _, ok := d.tasks.Get(cfg.ID); ok {
		return nil, nil, fmt.Errorf("task with ID %q is already running", cfg.ID)
	}

	var driverCfg TaskConfig
	if err := cfg.DecodeDriverConfig(&driverCfg); err != nil {
		return nil, nil, fmt.Errorf("failed to decode driver config: %w", err)
	}

	d.logger.Info("starting task", "task_name", cfg.Name, "alloc_id", cfg.AllocID)

	cName := containerName(cfg)

	// Pull the image if requested or if force_pull is set.
	pullTimeout := d.parsePullTimeout(&driverCfg)
	if err := d.pullImage(driverCfg.Image, pullTimeout, d.effectiveAuth(&driverCfg)); err != nil {
		return nil, nil, fmt.Errorf("image pull failed: %w", err)
	}

	// Build the `container run` argument list.
	runArgs, err := d.buildRunArgs(cfg, &driverCfg, cName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build container run args: %w", err)
	}

	d.logger.Debug("running container", "args", strings.Join(runArgs, " "))

	// Launch the container in detached mode.
	runCmd := exec.Command(d.containerPath(), runArgs...)
	var runStderr bytes.Buffer
	runCmd.Stderr = &runStderr
	if err := runCmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("container run failed: %w (stderr: %s)", err, runStderr.String())
	}

	// Build the task handle used to track the container.
	taskCtx, taskCancel := context.WithCancel(d.ctx)
	h := &taskHandle{
		containerName: cName,
		containerPath: d.containerPath(),
		logger:        d.logger.With("task_name", cfg.Name, "alloc_id", cfg.AllocID),
		taskConfig:    cfg,
		procState:     drivers.TaskStateRunning,
		startedAt:     time.Now(),
		doneCh:        make(chan struct{}),
		ctx:           taskCtx,
		cancel:        taskCancel,
	}

	driverState := TaskState{
		TaskConfig:    cfg,
		ContainerName: cName,
		StartedAt:     h.startedAt,
	}

	handle := drivers.NewTaskHandle(taskHandleVersion)
	handle.Config = cfg
	if err := handle.SetDriverState(&driverState); err != nil {
		taskCancel()
		return nil, nil, fmt.Errorf("failed to set driver state: %w", err)
	}

	d.tasks.Set(cfg.ID, h)

	// Start the log-forwarding goroutine (unless log collection is disabled).
	if !d.config.DisableLogCollection {
		go h.streamLogs(cfg.StdoutPath)
	}

	// Start the wait loop that detects container exit.
	go h.runWaitLoop(d.inspectContainer)

	return handle, nil, nil
}

// WaitTask returns a channel that receives the exit result when the task exits.
func (d *Driver) WaitTask(ctx context.Context, taskID string) (<-chan *drivers.ExitResult, error) {
	h, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	ch := make(chan *drivers.ExitResult, 1)
	go func() {
		select {
		case <-ctx.Done():
			ch <- &drivers.ExitResult{Err: ctx.Err(), ExitCode: -1}
		case <-h.doneCh:
			ch <- h.ExitResult()
		}
	}()
	return ch, nil
}

// StopTask sends a signal to the container to stop it, waiting up to timeout
// before force-killing it.
func (d *Driver) StopTask(taskID string, timeout time.Duration, signal string) error {
	h, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	if signal == "" {
		signal = "SIGTERM"
	}
	timeoutSec := int(timeout.Seconds())
	if timeoutSec < 1 {
		timeoutSec = 1
	}

	d.logger.Debug("stopping container",
		"container", h.containerName, "signal", signal, "timeout", timeoutSec)

	args := []string{
		"stop",
		"--signal", signal,
		"--time", strconv.Itoa(timeoutSec),
		h.containerName,
	}
	out, err := exec.CommandContext(d.ctx, d.containerPath(), args...).CombinedOutput()
	if err != nil {
		d.logger.Warn("container stop failed, attempting kill",
			"error", err, "output", string(out))
		// Fall back to a hard SIGKILL.
		killOut, killErr := exec.CommandContext(d.ctx, d.containerPath(),
			"kill", "--signal", "KILL", h.containerName).CombinedOutput()
		if killErr != nil {
			return fmt.Errorf("container kill failed: %w (output: %s)", killErr, string(killOut))
		}
	}

	return nil
}

// DestroyTask removes the container and cleans up the task handle.
func (d *Driver) DestroyTask(taskID string, force bool) error {
	h, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	if h.IsRunning() {
		if !force {
			return fmt.Errorf("cannot destroy running task; call StopTask first or pass force=true")
		}
		if err := d.StopTask(taskID, 5*time.Second, "SIGKILL"); err != nil {
			d.logger.Warn("force-stop failed during destroy", "error", err)
		}
	}

	// Cancel the task context so goroutines (wait loop, log stream) exit.
	h.cancel()
	<-h.doneCh

	// Remove the container if GC is enabled.
	if d.config.GC.Container {
		out, err := exec.CommandContext(d.ctx, d.containerPath(),
			"delete", "--force", h.containerName).CombinedOutput()
		if err != nil {
			d.logger.Warn("container delete failed",
				"error", err, "output", string(out))
		}
	}

	d.tasks.Delete(taskID)
	return nil
}

// InspectTask returns the current status of a task.
func (d *Driver) InspectTask(taskID string) (*drivers.TaskStatus, error) {
	h, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	h.stateLock.RLock()
	defer h.stateLock.RUnlock()

	status := &drivers.TaskStatus{
		ID:          taskID,
		Name:        h.taskConfig.Name,
		State:       h.procState,
		StartedAt:   h.startedAt,
		CompletedAt: h.completedAt,
		ExitResult:  h.exitResult,
		DriverAttributes: map[string]string{
			"container_name": h.containerName,
		},
	}
	return status, nil
}

// TaskStats returns a channel that sends resource usage statistics at the
// given interval.
func (d *Driver) TaskStats(
	ctx context.Context,
	taskID string,
	interval time.Duration,
) (<-chan *cstructs.TaskResourceUsage, error) {
	h, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	ch := make(chan *cstructs.TaskResourceUsage, 1)
	go h.collectStats(ctx, ch, interval)
	return ch, nil
}

// TaskEvents returns a channel that emits driver-level task events.
func (d *Driver) TaskEvents(ctx context.Context) (<-chan *drivers.TaskEvent, error) {
	return d.eventer.TaskEvents(ctx)
}

// SignalTask sends the given OS signal to the container's main process.
func (d *Driver) SignalTask(taskID string, signal string) error {
	h, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	if signal == "" {
		signal = "SIGTERM"
	}

	out, err := exec.CommandContext(d.ctx, d.containerPath(),
		"kill", "--signal", signal, h.containerName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("container kill --signal %s failed: %w (output: %s)",
			signal, err, string(out))
	}
	return nil
}

// ExecTask runs a command inside a running container.
func (d *Driver) ExecTask(
	taskID string,
	cmd []string,
	timeout time.Duration,
) (*drivers.ExecTaskResult, error) {
	h, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}
	if len(cmd) == 0 {
		return nil, fmt.Errorf("cmd must not be empty")
	}

	ctx := d.ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(d.ctx, timeout)
		defer cancel()
	}

	// Build: container exec <containerName> <cmd[0]> [cmd[1:]...]
	args := append([]string{"exec", h.containerName}, cmd...)
	execCmd := exec.CommandContext(ctx, d.containerPath(), args...)

	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	runErr := execCmd.Run()
	exitCode := 0
	if runErr != nil {
		exitCode = exitCodeFromError(runErr)
	}

	return &drivers.ExecTaskResult{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
		ExitResult: &drivers.ExitResult{
			ExitCode: exitCode,
		},
	}, nil
}

// RecoverTask re-attaches the driver to a container that was started before
// the plugin process was restarted.
func (d *Driver) RecoverTask(handle *drivers.TaskHandle) error {
	if handle == nil {
		return fmt.Errorf("error: handle cannot be nil")
	}
	if _, ok := d.tasks.Get(handle.Config.ID); ok {
		return nil // already tracked
	}

	var taskState TaskState
	if err := handle.GetDriverState(&taskState); err != nil {
		return fmt.Errorf("failed to decode task state from handle: %w", err)
	}

	// Verify the container is still present.
	info, err := d.inspectContainer(taskState.ContainerName)
	if err != nil {
		return fmt.Errorf("failed to inspect recovered container %q: %w",
			taskState.ContainerName, err)
	}

	taskCtx, taskCancel := context.WithCancel(d.ctx)
	h := &taskHandle{
		containerName: taskState.ContainerName,
		containerPath: d.containerPath(),
		logger: d.logger.With(
			"task_name", handle.Config.Name,
			"alloc_id", handle.Config.AllocID,
		),
		taskConfig: taskState.TaskConfig,
		startedAt:  taskState.StartedAt,
		doneCh:     make(chan struct{}),
		ctx:        taskCtx,
		cancel:     taskCancel,
	}

	if info.Status == "running" {
		h.procState = drivers.TaskStateRunning
	} else {
		h.procState = drivers.TaskStateExited
		h.exitResult = &drivers.ExitResult{ExitCode: info.ExitCode}
		h.completedAt = time.Now()
	}

	d.tasks.Set(handle.Config.ID, h)

	if h.procState == drivers.TaskStateRunning {
		if !d.config.DisableLogCollection {
			go h.streamLogs(handle.Config.StdoutPath)
		}
		go h.runWaitLoop(d.inspectContainer)
	} else {
		close(h.doneCh)
	}

	return nil
}

// ────────────────────────────────────────
// Internal helpers
// ────────────────────────────────────────

// containerPath returns the path to the container CLI binary.
func (d *Driver) containerPath() string {
	if d.config != nil && d.config.ContainerPath != "" {
		return d.config.ContainerPath
	}
	return defaultContainerPath
}

// pullImage runs `container image pull` for the given image reference.
func (d *Driver) pullImage(image string, timeout time.Duration, auth *AuthConfig) error {
	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()

	args := []string{"image", "pull"}

	// Log in first if credentials are provided.
	if auth != nil && auth.Username != "" {
		loginArgs := []string{"registry", "login"}
		if auth.ServerAddress != "" {
			loginArgs = append(loginArgs, auth.ServerAddress)
		}
		loginArgs = append(loginArgs, "--username", auth.Username)

		loginCmd := exec.CommandContext(ctx, d.containerPath(), loginArgs...)
		loginCmd.Stdin = strings.NewReader(auth.Password + "\n")
		if out, err := loginCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("registry login failed: %w (output: %s)", err, string(out))
		}
	}

	args = append(args, image)
	out, err := exec.CommandContext(ctx, d.containerPath(), args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("image pull %q failed: %w (output: %s)", image, err, string(out))
	}
	return nil
}

// inspectContainer runs `container inspect <name>` and returns parsed output.
func (d *Driver) inspectContainer(name string) (*inspectData, error) {
	out, err := exec.CommandContext(d.ctx, d.containerPath(), "inspect", name).Output()
	if err != nil {
		return nil, fmt.Errorf("container inspect %q: %w", name, err)
	}

	// `container inspect` can return either a JSON object or a JSON array.
	out = bytes.TrimSpace(out)

	if bytes.HasPrefix(out, []byte("[")) {
		var list []inspectData
		if err := json.Unmarshal(out, &list); err != nil {
			return nil, fmt.Errorf("failed to parse inspect JSON array: %w", err)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("inspect returned empty array for %q", name)
		}
		return &list[0], nil
	}

	var info inspectData
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("failed to parse inspect JSON: %w", err)
	}
	return &info, nil
}

// buildRunArgs constructs the argument slice for `container run`.
func (d *Driver) buildRunArgs(
	cfg *drivers.TaskConfig,
	driverCfg *TaskConfig,
	cName string,
) ([]string, error) {
	args := []string{
		"run",
		"--detach",
		"--name", cName,
	}

	// ── Resource limits ──────────────────────────────────────────────────────
	if cfg.Resources != nil && cfg.Resources.NomadResources != nil {
		mem := cfg.Resources.NomadResources.Memory.MemoryMB
		if mem > 0 {
			args = append(args, "--memory", fmt.Sprintf("%dMiB", mem))
		}
		if len(cfg.Resources.NomadResources.Cpu.ReservedCores) > 0 {
			args = append(args, "--cpus",
				strconv.Itoa(len(cfg.Resources.NomadResources.Cpu.ReservedCores)))
		}
	}

	// ── Environment variables ────────────────────────────────────────────────
	// Inject Nomad's runtime environment first, then task-specific overrides.
	for k, v := range cfg.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	for k, v := range driverCfg.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// ── Port publishing ───────────────────────────────────────────────────────
	if cfg.Resources != nil && cfg.Resources.Ports != nil {
		portLabelSet := make(map[string]struct{})
		for _, l := range driverCfg.Ports {
			portLabelSet[l] = struct{}{}
		}
		for _, p := range *cfg.Resources.Ports {
			if _, ok := portLabelSet[p.Label]; !ok {
				continue
			}
			to := p.To
			if to == 0 {
				to = p.Value
			}
			args = append(args, "-p", fmt.Sprintf("%d:%d", p.Value, to))
		}
	}

	// ── Volume mounts ────────────────────────────────────────────────────────
	if d.config.Volumes.Enabled {
		for _, v := range driverCfg.Volumes {
			args = append(args, "-v", v)
		}
	}

	// Honour Nomad-managed mounts (e.g. alloc/, local/, secrets/).
	for _, m := range cfg.Mounts {
		spec := fmt.Sprintf("%s:%s", m.HostPath, m.TaskPath)
		if m.Readonly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}

	// ── Tmpfs ────────────────────────────────────────────────────────────────
	for _, t := range driverCfg.Tmpfs {
		args = append(args, "--tmpfs", t)
	}

	// ── Networking ───────────────────────────────────────────────────────────
	if driverCfg.NetworkMode != "" {
		args = append(args, "--network", driverCfg.NetworkMode)
	}

	// ── DNS ──────────────────────────────────────────────────────────────────
	for _, ns := range driverCfg.DNS {
		args = append(args, "--dns", ns)
	}
	for _, s := range driverCfg.DNSSearch {
		args = append(args, "--dns-search", s)
	}
	for _, o := range driverCfg.DNSOptions {
		args = append(args, "--dns-option", o)
	}

	// ── Labels ───────────────────────────────────────────────────────────────
	for k, v := range driverCfg.Labels {
		args = append(args, "-l", fmt.Sprintf("%s=%s", k, v))
	}
	// Extra labels requested by the operator-level plugin config.
	for _, pattern := range d.config.ExtraLabels {
		for _, kv := range d.nomadLabels(cfg, pattern) {
			args = append(args, "-l", kv)
		}
	}

	// ── Capabilities ─────────────────────────────────────────────────────────
	for _, cap := range driverCfg.CapAdd {
		args = append(args, "--cap-add", cap)
	}
	for _, cap := range driverCfg.CapDrop {
		args = append(args, "--cap-drop", cap)
	}

	// ── User ─────────────────────────────────────────────────────────────────
	if driverCfg.User != "" {
		args = append(args, "--user", driverCfg.User)
	} else if cfg.User != "" {
		args = append(args, "--user", cfg.User)
	}

	// ── Working directory ────────────────────────────────────────────────────
	if driverCfg.WorkingDir != "" {
		args = append(args, "--workdir", driverCfg.WorkingDir)
	}

	// ── Hostname ─────────────────────────────────────────────────────────────
	if driverCfg.Hostname != "" {
		args = append(args, "--hostname", driverCfg.Hostname)
	}

	// ── Entrypoint ───────────────────────────────────────────────────────────
	if driverCfg.Entrypoint != "" {
		args = append(args, "--entrypoint", driverCfg.Entrypoint)
	}

	// ── Init process ─────────────────────────────────────────────────────────
	if driverCfg.Init {
		args = append(args, "--init")
	}

	// ── Read-only root ────────────────────────────────────────────────────────
	if driverCfg.ReadonlyRootfs {
		args = append(args, "--read-only")
	}

	// ── TTY ──────────────────────────────────────────────────────────────────
	if driverCfg.TTY {
		args = append(args, "--tty")
	}

	// ── macOS-specific flags ──────────────────────────────────────────────────
	if driverCfg.Rosetta {
		args = append(args, "--rosetta")
	}
	if driverCfg.SSHAgent {
		args = append(args, "--ssh")
	}
	if driverCfg.Virtualization {
		args = append(args, "--virtualization")
	}

	// ── Image ─────────────────────────────────────────────────────────────────
	args = append(args, driverCfg.Image)

	// ── Command and arguments ─────────────────────────────────────────────────
	if driverCfg.Command != "" {
		args = append(args, driverCfg.Command)
	}
	args = append(args, driverCfg.Args...)

	return args, nil
}

// parsePullTimeout returns the effective image pull timeout for a task.
func (d *Driver) parsePullTimeout(driverCfg *TaskConfig) time.Duration {
	raw := driverCfg.ImagePullTimeout
	if raw == "" {
		raw = d.config.ImagePullTimeout
	}
	if raw == "" {
		return defaultImagePullTimeout
	}
	dur, err := time.ParseDuration(raw)
	if err != nil {
		d.logger.Warn("invalid image_pull_timeout, using default",
			"value", raw, "default", defaultImagePullTimeout)
		return defaultImagePullTimeout
	}
	return dur
}

// effectiveAuth returns the auth config to use for pulling, preferring the
// task-level config over the plugin-level config.
func (d *Driver) effectiveAuth(driverCfg *TaskConfig) *AuthConfig {
	if driverCfg.Auth != nil {
		return driverCfg.Auth
	}
	return d.config.Auth
}

// nomadLabels returns "key=value" label strings for the given glob pattern.
func (d *Driver) nomadLabels(cfg *drivers.TaskConfig, pattern string) []string {
	candidates := map[string]string{
		"job_name":        cfg.JobName,
		"task_group_name": cfg.TaskGroupName,
		"task_name":       cfg.Name,
		"namespace":       cfg.Namespace,
		"node_id":         cfg.NodeID,
	}
	var result []string
	for k, v := range candidates {
		matched, _ := matchGlob(pattern, k)
		if matched {
			result = append(result, fmt.Sprintf("nomad.%s=%s", k, v))
		}
	}
	return result
}

// ────────────────────────────────────────
// Package-level utilities used by handle.go
// ────────────────────────────────────────

// containerName derives a deterministic, sanitised container name from a Nomad
// task config.  Format: nomad-<taskName>-<allocID[:8]>
func containerName(cfg *drivers.TaskConfig) string {
	return fmt.Sprintf("nomad-%s-%s",
		sanitizeLabel(cfg.Name),
		cfg.AllocID[:8],
	)
}

var nonAlphanumRe = regexp.MustCompile(`[^a-zA-Z0-9\-_]`)

// sanitizeLabel replaces characters that are not valid in container names with
// hyphens.
func sanitizeLabel(s string) string {
	return nonAlphanumRe.ReplaceAllString(s, "-")
}

// parseStats parses `container stats --no-stream --format json` output.
func parseStats(data []byte, containerName string) (*statsData, error) {
	var list []statsData
	if err := json.Unmarshal(data, &list); err != nil {
		// Also try a plain object (single container).
		var single statsData
		if err2 := json.Unmarshal(data, &single); err2 != nil {
			return nil, fmt.Errorf("failed to parse stats JSON: %w", err)
		}
		return &single, nil
	}
	for _, s := range list {
		if s.Name == containerName || s.ID == containerName {
			return &s, nil
		}
	}
	if len(list) > 0 {
		return &list[0], nil
	}
	return nil, fmt.Errorf("no stats returned for container %q", containerName)
}

// statsToTaskResourceUsage converts container stats to a Nomad usage struct.
func statsToTaskResourceUsage(s *statsData) *cstructs.TaskResourceUsage {
	return &cstructs.TaskResourceUsage{
		ResourceUsage: &cstructs.ResourceUsage{
			MemoryStats: &cstructs.MemoryStats{
				RSS:      s.MemUsage,
				Usage:    s.MemUsage,
				Measured: []string{"RSS", "Usage"},
			},
			CpuStats: &cstructs.CpuStats{
				Percent:  s.CPUPercent,
				Measured: []string{"Percent"},
			},
		},
		Timestamp: time.Now().UTC().UnixNano(),
	}
}

// exitCodeFromError extracts the exit code from an exec.ExitError.
func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus); ok {
			return ws.ExitStatus()
		}
	}
	return 1
}

// signum converts a signal name string to its integer value.
func signum(signal string) syscall.Signal {
	switch strings.ToUpper(signal) {
	case "SIGTERM":
		return syscall.SIGTERM
	case "SIGKILL":
		return syscall.SIGKILL
	case "SIGHUP":
		return syscall.SIGHUP
	case "SIGINT":
		return syscall.SIGINT
	case "SIGUSR1":
		return syscall.SIGUSR1
	case "SIGUSR2":
		return syscall.SIGUSR2
	default:
		return syscall.SIGTERM
	}
}

// matchGlob is a simple glob matcher that supports only the '*' wildcard.
func matchGlob(pattern, s string) (bool, error) {
	pattern = strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, `.*`)
	return regexp.MatchString("^"+pattern+"$", s)
}


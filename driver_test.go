// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestDriver() *Driver {
	return NewDriver(hclog.NewNullLogger())
}

func minimalTaskConfig(name, allocID string) *drivers.TaskConfig {
	return &drivers.TaskConfig{
		Name:    name,
		AllocID: allocID,
	}
}

// assertContains checks that args contains the given flag somewhere.
func assertContains(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args %v does not contain %q", args, want)
}

// assertNotContains checks that args does NOT contain the given flag.
func assertNotContains(t *testing.T, args []string, unwanted string) {
	t.Helper()
	for _, a := range args {
		if a == unwanted {
			t.Errorf("args %v unexpectedly contains %q", args, unwanted)
			return
		}
	}
}

// assertContainsPair checks that args contains the consecutive pair flag, val.
func assertContainsPair(t *testing.T, args []string, flag, val string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return
		}
	}
	t.Errorf("args %v does not contain pair %q %q", args, flag, val)
}

// ── containerName ─────────────────────────────────────────────────────────────

func TestContainerName(t *testing.T) {
	cfg := &drivers.TaskConfig{Name: "web", AllocID: "abc12345xyz"}
	got := containerName(cfg)
	if got != "nomad-web-abc12345" {
		t.Errorf("containerName() = %q; want %q", got, "nomad-web-abc12345")
	}
}

func TestContainerName_SpecialChars(t *testing.T) {
	cfg := &drivers.TaskConfig{Name: "web.server", AllocID: "abc12345xyz"}
	got := containerName(cfg)
	if got != "nomad-web-server-abc12345" {
		t.Errorf("containerName() = %q; want %q", got, "nomad-web-server-abc12345")
	}
}

// ── sanitizeLabel ─────────────────────────────────────────────────────────────

func TestSanitizeLabel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with space", "with-space"},
		{"with.dot", "with-dot"},
		{"with/slash", "with-slash"},
		{"already-valid_123", "already-valid_123"},
		{"MixedCase", "MixedCase"},
		{"a:b", "a-b"},
	}
	for _, tt := range tests {
		got := sanitizeLabel(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeLabel(%q) = %q; want %q", tt.input, got, tt.want)
		}
	}
}

// ── parseStats ────────────────────────────────────────────────────────────────

func TestParseStats_JSONArray_MatchByName(t *testing.T) {
	data := []byte(`[
		{"id":"id1","name":"other","cpu_percent":1.0,"mem_usage":100},
		{"id":"id2","name":"my-container","cpu_percent":12.5,"mem_usage":1048576}
	]`)
	s, err := parseStats(data, "my-container")
	if err != nil {
		t.Fatalf("parseStats() error = %v", err)
	}
	if s.CPUPercent != 12.5 {
		t.Errorf("CPUPercent = %v; want 12.5", s.CPUPercent)
	}
	if s.MemUsage != 1048576 {
		t.Errorf("MemUsage = %v; want 1048576", s.MemUsage)
	}
}

func TestParseStats_JSONArray_MatchByID(t *testing.T) {
	data := []byte(`[{"id":"target-id","name":"c","cpu_percent":5.0,"mem_usage":2048}]`)
	s, err := parseStats(data, "target-id")
	if err != nil {
		t.Fatalf("parseStats() error = %v", err)
	}
	if s.ID != "target-id" {
		t.Errorf("ID = %q; want target-id", s.ID)
	}
}

func TestParseStats_JSONArray_NoMatch_ReturnFirst(t *testing.T) {
	data := []byte(`[{"id":"id1","name":"c1","cpu_percent":1.0,"mem_usage":100}]`)
	s, err := parseStats(data, "nonexistent")
	if err != nil {
		t.Fatalf("parseStats() error = %v", err)
	}
	// Falls back to first element when no name/id match.
	if s.Name != "c1" {
		t.Errorf("expected fallback to first element with Name=c1, got %q", s.Name)
	}
}

func TestParseStats_JSONObject(t *testing.T) {
	data := []byte(`{"id":"abc","name":"my-container","cpu_percent":5.0,"mem_usage":2097152}`)
	s, err := parseStats(data, "my-container")
	if err != nil {
		t.Fatalf("parseStats() error = %v", err)
	}
	if s.CPUPercent != 5.0 {
		t.Errorf("CPUPercent = %v; want 5.0", s.CPUPercent)
	}
}

func TestParseStats_EmptyArray(t *testing.T) {
	_, err := parseStats([]byte(`[]`), "c")
	if err == nil {
		t.Error("expected error for empty array, got nil")
	}
}

func TestParseStats_InvalidJSON(t *testing.T) {
	_, err := parseStats([]byte("not-json"), "c")
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// ── statsToTaskResourceUsage ──────────────────────────────────────────────────

func TestStatsToTaskResourceUsage(t *testing.T) {
	s := &statsData{
		Name:       "c",
		CPUPercent: 42.0,
		MemUsage:   512,
	}
	u := statsToTaskResourceUsage(s)
	if u.ResourceUsage.CpuStats.Percent != 42.0 {
		t.Errorf("CpuStats.Percent = %v; want 42.0", u.ResourceUsage.CpuStats.Percent)
	}
	if u.ResourceUsage.MemoryStats.RSS != 512 {
		t.Errorf("MemoryStats.RSS = %v; want 512", u.ResourceUsage.MemoryStats.RSS)
	}
	if u.ResourceUsage.MemoryStats.Usage != 512 {
		t.Errorf("MemoryStats.Usage = %v; want 512", u.ResourceUsage.MemoryStats.Usage)
	}
	if u.Timestamp == 0 {
		t.Error("Timestamp should not be zero")
	}
	measured := u.ResourceUsage.MemoryStats.Measured
	if len(measured) == 0 {
		t.Error("MemoryStats.Measured should not be empty")
	}
}

// ── exitCodeFromError ─────────────────────────────────────────────────────────

func TestExitCodeFromError_Nil(t *testing.T) {
	if got := exitCodeFromError(nil); got != 0 {
		t.Errorf("exitCodeFromError(nil) = %v; want 0", got)
	}
}

func TestExitCodeFromError_GenericError(t *testing.T) {
	if got := exitCodeFromError(errors.New("oops")); got != 1 {
		t.Errorf("exitCodeFromError(generic) = %v; want 1", got)
	}
}

func TestExitCodeFromError_ExitError(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 2")
	err := cmd.Run()
	if err == nil {
		t.Skip("expected non-zero exit from sh -c 'exit 2'")
	}
	if got := exitCodeFromError(err); got != 2 {
		t.Errorf("exitCodeFromError(exit 2) = %v; want 2", got)
	}
}

// ── signum ────────────────────────────────────────────────────────────────────

func TestSignum(t *testing.T) {
	tests := []struct {
		signal string
		want   syscall.Signal
	}{
		{"SIGTERM", syscall.SIGTERM},
		{"SIGKILL", syscall.SIGKILL},
		{"SIGHUP", syscall.SIGHUP},
		{"SIGINT", syscall.SIGINT},
		{"SIGUSR1", syscall.SIGUSR1},
		{"SIGUSR2", syscall.SIGUSR2},
		{"sigterm", syscall.SIGTERM}, // case-insensitive
		{"UNKNOWN", syscall.SIGTERM}, // unknown falls back to SIGTERM
		{"", syscall.SIGTERM},        // empty falls back to SIGTERM
	}
	for _, tt := range tests {
		got := signum(tt.signal)
		if got != tt.want {
			t.Errorf("signum(%q) = %v; want %v", tt.signal, got, tt.want)
		}
	}
}

// ── matchGlob ─────────────────────────────────────────────────────────────────

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		s       string
		want    bool
	}{
		{"*", "anything", true},
		{"task*", "task_name", true},
		{"task*", "job_name", false},
		{"job_name", "job_name", true},
		{"job_name", "job_names", false},
		{"*name", "job_name", true},
		{"*name", "job_names", false},
		{"node*", "node_id", true},
		{"node*", "namespace", false},
	}
	for _, tt := range tests {
		got, err := matchGlob(tt.pattern, tt.s)
		if err != nil {
			t.Errorf("matchGlob(%q, %q) unexpected error: %v", tt.pattern, tt.s, err)
			continue
		}
		if got != tt.want {
			t.Errorf("matchGlob(%q, %q) = %v; want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}

// ── nomadLabels ───────────────────────────────────────────────────────────────

func TestNomadLabels_Wildcard(t *testing.T) {
	d := newTestDriver()
	cfg := &drivers.TaskConfig{
		JobName:       "my-job",
		TaskGroupName: "web",
		Name:          "nginx",
		Namespace:     "default",
		NodeID:        "node-1",
	}
	labels := d.nomadLabels(cfg, "*")
	if len(labels) != 5 {
		t.Errorf("nomadLabels(*) returned %d labels; want 5", len(labels))
	}
}

func TestNomadLabels_Specific(t *testing.T) {
	d := newTestDriver()
	cfg := &drivers.TaskConfig{JobName: "my-job", Name: "nginx"}
	labels := d.nomadLabels(cfg, "job_name")
	if len(labels) != 1 {
		t.Fatalf("nomadLabels(job_name) returned %d; want 1", len(labels))
	}
	if labels[0] != "nomad.job_name=my-job" {
		t.Errorf("label = %q; want %q", labels[0], "nomad.job_name=my-job")
	}
}

func TestNomadLabels_Prefix(t *testing.T) {
	d := newTestDriver()
	cfg := &drivers.TaskConfig{Name: "nginx", TaskGroupName: "web"}
	// "task*" should match task_name and task_group_name.
	labels := d.nomadLabels(cfg, "task*")
	if len(labels) != 2 {
		t.Errorf("nomadLabels(task*) returned %d; want 2", len(labels))
	}
}

func TestNomadLabels_NoMatch(t *testing.T) {
	d := newTestDriver()
	cfg := &drivers.TaskConfig{Name: "nginx"}
	labels := d.nomadLabels(cfg, "no_match")
	if len(labels) != 0 {
		t.Errorf("nomadLabels(no_match) returned %d; want 0", len(labels))
	}
}

// ── parsePullTimeout ──────────────────────────────────────────────────────────

func TestParsePullTimeout(t *testing.T) {
	tests := []struct {
		taskTimeout   string
		pluginTimeout string
		want          time.Duration
	}{
		{"10m", "5m", 10 * time.Minute},          // task-level overrides plugin
		{"", "3m", 3 * time.Minute},              // plugin-level fallback
		{"", "", defaultImagePullTimeout},        // global default
		{"invalid", "", defaultImagePullTimeout}, // bad value falls back to default
		{"", "invalid", defaultImagePullTimeout}, // bad plugin value falls back
	}
	for _, tt := range tests {
		d := newTestDriver()
		d.config.ImagePullTimeout = tt.pluginTimeout
		driverCfg := &TaskConfig{ImagePullTimeout: tt.taskTimeout}
		got := d.parsePullTimeout(driverCfg)
		if got != tt.want {
			t.Errorf("parsePullTimeout(task=%q, plugin=%q) = %v; want %v",
				tt.taskTimeout, tt.pluginTimeout, got, tt.want)
		}
	}
}

// ── effectiveAuth ─────────────────────────────────────────────────────────────

func TestEffectiveAuth_TaskWins(t *testing.T) {
	pluginAuth := &AuthConfig{Username: "plugin-user", Password: "plugin-pass"}
	taskAuth := &AuthConfig{Username: "task-user", Password: "task-pass"}
	d := newTestDriver()
	d.config.Auth = pluginAuth

	got := d.effectiveAuth(&TaskConfig{Auth: taskAuth})
	if got.Username != "task-user" {
		t.Errorf("expected task auth username, got %q", got.Username)
	}
}

func TestEffectiveAuth_PluginFallback(t *testing.T) {
	pluginAuth := &AuthConfig{Username: "plugin-user", Password: "plugin-pass"}
	d := newTestDriver()
	d.config.Auth = pluginAuth

	got := d.effectiveAuth(&TaskConfig{})
	if got.Username != "plugin-user" {
		t.Errorf("expected plugin auth username, got %q", got.Username)
	}
}

func TestEffectiveAuth_NilWhenNoneConfigured(t *testing.T) {
	d := newTestDriver()
	d.config.Auth = nil

	got := d.effectiveAuth(&TaskConfig{})
	if got != nil {
		t.Errorf("expected nil auth, got %+v", got)
	}
}

// ── SetConfig defaults ────────────────────────────────────────────────────────

func TestSetConfigDefaults(t *testing.T) {
	d := newTestDriver()
	err := d.SetConfig(&base.Config{})
	if err != nil {
		t.Fatalf("SetConfig() error = %v", err)
	}
	if d.config.ContainerPath != defaultContainerPath {
		t.Errorf("ContainerPath = %q; want %q", d.config.ContainerPath, defaultContainerPath)
	}
	if d.config.ImagePullTimeout != "5m" {
		t.Errorf("ImagePullTimeout = %q; want %q", d.config.ImagePullTimeout, "5m")
	}
	if !d.config.GC.Container {
		t.Error("GC.Container should default to true")
	}
	if !d.config.Volumes.Enabled {
		t.Error("Volumes.Enabled should default to true")
	}
}

// ── PluginInfo ────────────────────────────────────────────────────────────────

func TestPluginInfo(t *testing.T) {
	d := newTestDriver()
	info, err := d.PluginInfo()
	if err != nil {
		t.Fatalf("PluginInfo() error = %v", err)
	}
	if info.Name != PluginName {
		t.Errorf("Name = %q; want %q", info.Name, PluginName)
	}
	if info.Type != "driver" {
		t.Errorf("Type = %q; want driver", info.Type)
	}
	if len(info.PluginApiVersions) == 0 {
		t.Error("PluginApiVersions should not be empty")
	}
}

// ── Capabilities ──────────────────────────────────────────────────────────────

func TestCapabilities(t *testing.T) {
	d := newTestDriver()
	caps, err := d.Capabilities()
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if !caps.SendSignals {
		t.Error("expected SendSignals = true")
	}
	if !caps.Exec {
		t.Error("expected Exec = true")
	}
	if len(caps.NetIsolationModes) == 0 {
		t.Error("expected at least one NetIsolationMode")
	}
}

// ── buildFingerprint ──────────────────────────────────────────────────────────

func TestBuildFingerprint_BinaryNotFound(t *testing.T) {
	d := newTestDriver()
	d.config.ContainerPath = "/nonexistent/path/container"
	fp := d.buildFingerprint()
	if fp.Health != drivers.HealthStateUndetected {
		t.Errorf("Health = %v; want HealthStateUndetected", fp.Health)
	}
	if fp.HealthDescription == "" {
		t.Error("HealthDescription should not be empty")
	}
}

// ── buildRunArgs ──────────────────────────────────────────────────────────────

func TestBuildRunArgs_Minimal(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx:latest"}

	args, err := d.buildRunArgs(cfg, driverCfg, "nomad-web-alloc123")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContains(t, args, "run")
	assertContains(t, args, "--detach")
	assertContainsPair(t, args, "--name", "nomad-web-alloc123")
	assertContains(t, args, "nginx:latest")
}

func TestBuildRunArgs_CommandAndArgs(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{
		Image:   "alpine:latest",
		Command: "/bin/sh",
		Args:    []string{"-c", "echo hello"},
	}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContains(t, args, "/bin/sh")
	assertContains(t, args, "-c")
	assertContains(t, args, "echo hello")
}

func TestBuildRunArgs_Entrypoint(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx", Entrypoint: "/docker-entrypoint.sh"}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "--entrypoint", "/docker-entrypoint.sh")
}

func TestBuildRunArgs_EnvVars(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	cfg.Env = map[string]string{"NOMAD_VAR": "val1"}
	driverCfg := &TaskConfig{
		Image: "nginx",
		Env:   map[string]string{"APP_ENV": "prod"},
	}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "-e", "NOMAD_VAR=val1")
	assertContainsPair(t, args, "-e", "APP_ENV=prod")
}

func TestBuildRunArgs_Volumes_Enabled(t *testing.T) {
	d := newTestDriver()
	d.config.Volumes.Enabled = true
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{
		Image:   "nginx",
		Volumes: []string{"/host:/container"},
	}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "-v", "/host:/container")
}

func TestBuildRunArgs_Volumes_Disabled(t *testing.T) {
	d := newTestDriver()
	d.config.Volumes.Enabled = false
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{
		Image:   "nginx",
		Volumes: []string{"/host:/container"},
	}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	// -v /host:/container should be absent when volumes are disabled.
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && args[i+1] == "/host:/container" {
			t.Error("expected volume to be omitted when volumes.enabled = false")
		}
	}
}

func TestBuildRunArgs_Tmpfs(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx", Tmpfs: []string{"/tmp", "/run"}}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "--tmpfs", "/tmp")
	assertContainsPair(t, args, "--tmpfs", "/run")
}

func TestBuildRunArgs_NetworkMode(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx", NetworkMode: "host"}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "--network", "host")
}

func TestBuildRunArgs_DNS(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{
		Image:      "nginx",
		DNS:        []string{"8.8.8.8"},
		DNSSearch:  []string{"example.com"},
		DNSOptions: []string{"ndots:5"},
	}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "--dns", "8.8.8.8")
	assertContainsPair(t, args, "--dns-search", "example.com")
	assertContainsPair(t, args, "--dns-option", "ndots:5")
}

func TestBuildRunArgs_Capabilities(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{
		Image:   "nginx",
		CapAdd:  []string{"NET_ADMIN"},
		CapDrop: []string{"ALL"},
	}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "--cap-add", "NET_ADMIN")
	assertContainsPair(t, args, "--cap-drop", "ALL")
}

func TestBuildRunArgs_UserFromDriverConfig(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	cfg.User = "nomad"
	driverCfg := &TaskConfig{Image: "nginx", User: "override-user"}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	// Driver config user takes precedence over TaskConfig.User.
	assertContainsPair(t, args, "--user", "override-user")
}

func TestBuildRunArgs_UserFromTaskConfig(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	cfg.User = "nomad"
	driverCfg := &TaskConfig{Image: "nginx"} // no User in driver config

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "--user", "nomad")
}

func TestBuildRunArgs_WorkingDirAndHostname(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{
		Image:      "nginx",
		WorkingDir: "/app",
		Hostname:   "myhost",
	}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "--workdir", "/app")
	assertContainsPair(t, args, "--hostname", "myhost")
}

func TestBuildRunArgs_Init(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx", Init: true}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContains(t, args, "--init")
}

func TestBuildRunArgs_ReadonlyRootfs(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx", ReadonlyRootfs: true}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContains(t, args, "--read-only")
}

func TestBuildRunArgs_TTY(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx", TTY: true}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContains(t, args, "--tty")
}

func TestBuildRunArgs_Rosetta(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx", Rosetta: true}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContains(t, args, "--rosetta")
}

func TestBuildRunArgs_SSHAgent(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx", SSHAgent: true}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContains(t, args, "--ssh")
}

func TestBuildRunArgs_Virtualization(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx", Virtualization: true}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContains(t, args, "--virtualization")
}

func TestBuildRunArgs_BoolFlagsOff(t *testing.T) {
	// When all bool flags are false (default), the corresponding CLI flags
	// must NOT be present.
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{Image: "nginx"} // all bools default to false

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	for _, unwanted := range []string{
		"--rosetta", "--ssh", "--virtualization", "--init",
		"--read-only", "--tty",
	} {
		assertNotContains(t, args, unwanted)
	}
}

func TestBuildRunArgs_Labels(t *testing.T) {
	d := newTestDriver()
	cfg := minimalTaskConfig("web", "alloc1234")
	driverCfg := &TaskConfig{
		Image:  "nginx",
		Labels: map[string]string{"env": "prod"},
	}

	args, err := d.buildRunArgs(cfg, driverCfg, "c")
	if err != nil {
		t.Fatalf("buildRunArgs() error = %v", err)
	}
	assertContainsPair(t, args, "-l", "env=prod")
}

// ── inspectData JSON unmarshaling ─────────────────────────────────────────────

func TestInspectData_Object(t *testing.T) {
	data := []byte(`{
		"id": "abc123",
		"name": "nomad-web-abc12345",
		"state": {"status": "running", "exitCode": 0, "pid": 42},
		"image": "nginx:latest"
	}`)
	var info inspectData
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if info.ID != "abc123" {
		t.Errorf("ID = %q; want abc123", info.ID)
	}
	if info.State.Status != "running" {
		t.Errorf("State.Status = %q; want running", info.State.Status)
	}
	if info.State.Pid != 42 {
		t.Errorf("State.Pid = %v; want 42", info.State.Pid)
	}
}

func TestInspectData_Exited(t *testing.T) {
	data := []byte(`{"id":"x","name":"c","state":{"status":"stopped","exitCode":1},"image":"alpine"}`)
	var info inspectData
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if info.State.Status != "stopped" {
		t.Errorf("State.Status = %q; want stopped", info.State.Status)
	}
	if info.State.ExitCode != 1 {
		t.Errorf("State.ExitCode = %v; want 1", info.State.ExitCode)
	}
}

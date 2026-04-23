// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"github.com/hashicorp/nomad/plugins/shared/hclspec"
)

// configSpec is the hclspec for the driver-level plugin configuration block
// that operators set in the Nomad client config:
//
//	plugin "nomad-driver-container" {
//	  config { ... }
//	}
var configSpec = hclspec.NewObject(map[string]*hclspec.Spec{
	// enabled controls whether the driver is active on this client.
	"enabled": hclspec.NewDefault(
		hclspec.NewAttr("enabled", "bool", false),
		hclspec.NewLiteral("true"),
	),

	// container_path is the path to the Apple container CLI binary.
	"container_path": hclspec.NewDefault(
		hclspec.NewAttr("container_path", "string", false),
		hclspec.NewLiteral(`"/usr/local/bin/container"`),
	),

	// disable_log_collection disables Nomad from collecting logs for tasks
	// managed by this driver. Useful when logs are handled externally.
	"disable_log_collection": hclspec.NewDefault(
		hclspec.NewAttr("disable_log_collection", "bool", false),
		hclspec.NewLiteral("false"),
	),

	// extra_labels automatically appends Nomad metadata labels to containers.
	// Supported values: job_name, job_id, task_group_name, task_name,
	// namespace, node_name, node_id.  Glob patterns (e.g. "task*") are
	// accepted.
	"extra_labels": hclspec.NewAttr("extra_labels", "list(string)", false),

	// image_pull_timeout is the maximum time to wait for an image pull.
	"image_pull_timeout": hclspec.NewDefault(
		hclspec.NewAttr("image_pull_timeout", "string", false),
		hclspec.NewLiteral(`"5m"`),
	),

	// gc controls garbage collection of containers after tasks exit.
	"gc": hclspec.NewBlock("gc", false, hclspec.NewObject(map[string]*hclspec.Spec{
		"container": hclspec.NewDefault(
			hclspec.NewAttr("container", "bool", false),
			hclspec.NewLiteral("true"),
		),
	})),

	// volumes controls whether host bind-mounts are permitted in tasks.
	"volumes": hclspec.NewBlock("volumes", false, hclspec.NewObject(map[string]*hclspec.Spec{
		"enabled": hclspec.NewDefault(
			hclspec.NewAttr("enabled", "bool", false),
			hclspec.NewLiteral("true"),
		),
	})),

	// auth provides default registry credentials for image pulls.
	"auth": hclspec.NewBlock("auth", false, hclspec.NewObject(map[string]*hclspec.Spec{
		"username":       hclspec.NewAttr("username", "string", true),
		"password":       hclspec.NewAttr("password", "string", true),
		"server_address": hclspec.NewAttr("server_address", "string", false),
	})),
})

// taskConfigSpec is the hclspec for the per-task config block:
//
//	task "web" {
//	  driver = "container"
//	  config { ... }
//	}
var taskConfigSpec = hclspec.NewObject(map[string]*hclspec.Spec{
	// image is the OCI image reference to run (required).
	// Accepts any reference valid for `container run`, e.g.:
	//   "nginx:latest", "registry.io/org/image:tag"
	"image": hclspec.NewAttr("image", "string", true),

	// command overrides the image's default command (CMD).
	"command": hclspec.NewAttr("command", "string", false),

	// args are passed as arguments to the container's entry process.
	"args": hclspec.NewAttr("args", "list(string)", false),

	// entrypoint overrides the image's ENTRYPOINT.
	"entrypoint": hclspec.NewAttr("entrypoint", "string", false),

	// working_dir sets the working directory inside the container.
	"working_dir": hclspec.NewAttr("working_dir", "string", false),

	// env sets extra environment variables inside the container.
	// Nomad's runtime environment variables are always injected first.
	"env": hclspec.NewBlockAttrs("env", "string", false),

	// volumes is a list of host:container[:options] bind-mount specs.
	// Requires volumes.enabled = true in the plugin configuration.
	"volumes": hclspec.NewAttr("volumes", "list(string)", false),

	// tmpfs is a list of container paths to mount as tmpfs.
	"tmpfs": hclspec.NewAttr("tmpfs", "list(string)", false),

	// ports references port labels defined in the task group network block.
	// The driver maps them to -p host:container publish rules.
	"ports": hclspec.NewAttr("ports", "list(string)", false),

	// network_mode controls the container's network configuration.
	// Supported values: default, host, none, or a user-defined network name.
	// On macOS 26+ the full network management API is available.
	"network_mode": hclspec.NewAttr("network_mode", "string", false),

	// hostname sets the container hostname.
	"hostname": hclspec.NewAttr("hostname", "string", false),

	// user sets the user or uid[:gid] for the container process.
	"user": hclspec.NewAttr("user", "string", false),

	// labels sets key=value metadata labels on the container.
	"labels": hclspec.NewBlockAttrs("labels", "string", false),

	// cap_add adds Linux capabilities to the container process
	// (e.g. "NET_ADMIN", "SYS_TIME").
	"cap_add": hclspec.NewAttr("cap_add", "list(string)", false),

	// cap_drop drops Linux capabilities from the container process.
	"cap_drop": hclspec.NewAttr("cap_drop", "list(string)", false),

	// readonly_rootfs mounts the container root filesystem as read-only.
	"readonly_rootfs": hclspec.NewDefault(
		hclspec.NewAttr("readonly_rootfs", "bool", false),
		hclspec.NewLiteral("false"),
	),

	// init runs a minimal init process inside the container that forwards
	// signals and reaps zombie processes.
	"init": hclspec.NewDefault(
		hclspec.NewAttr("init", "bool", false),
		hclspec.NewLiteral("false"),
	),

	// tty allocates a pseudo-TTY for the container process.
	"tty": hclspec.NewDefault(
		hclspec.NewAttr("tty", "bool", false),
		hclspec.NewLiteral("false"),
	),

	// dns sets custom DNS server addresses for the container.
	"dns": hclspec.NewAttr("dns", "list(string)", false),

	// dns_search sets DNS search domains.
	"dns_search": hclspec.NewAttr("dns_search", "list(string)", false),

	// dns_options sets DNS resolver options (e.g. "ndots:5").
	"dns_options": hclspec.NewAttr("dns_options", "list(string)", false),

	// rosetta enables Rosetta 2 translation inside the container, allowing
	// x86_64 Linux binaries to run on Apple Silicon. macOS 26+ only.
	"rosetta": hclspec.NewDefault(
		hclspec.NewAttr("rosetta", "bool", false),
		hclspec.NewLiteral("false"),
	),

	// force_pull always pulls the latest image before starting the container,
	// even if a local copy already exists.
	"force_pull": hclspec.NewDefault(
		hclspec.NewAttr("force_pull", "bool", false),
		hclspec.NewLiteral("false"),
	),

	// image_pull_timeout overrides the plugin-level pull timeout.
	"image_pull_timeout": hclspec.NewAttr("image_pull_timeout", "string", false),

	// auth provides per-task registry credentials for image pulls.
	"auth": hclspec.NewBlock("auth", false, hclspec.NewObject(map[string]*hclspec.Spec{
		"username":       hclspec.NewAttr("username", "string", true),
		"password":       hclspec.NewAttr("password", "string", true),
		"server_address": hclspec.NewAttr("server_address", "string", false),
	})),

	// ssh_agent forwards the host SSH agent socket into the container.
	"ssh_agent": hclspec.NewDefault(
		hclspec.NewAttr("ssh_agent", "bool", false),
		hclspec.NewLiteral("false"),
	),

	// virtualization exposes nested virtualisation capabilities to the
	// container (requires both host and guest support).
	"virtualization": hclspec.NewDefault(
		hclspec.NewAttr("virtualization", "bool", false),
		hclspec.NewLiteral("false"),
	),
})

// PluginConfig holds configuration set by the Nomad operator in the
// plugin{} block of the client agent configuration.
type PluginConfig struct {
	Enabled              bool         `codec:"enabled"`
	ContainerPath        string       `codec:"container_path"`
	DisableLogCollection bool         `codec:"disable_log_collection"`
	ExtraLabels          []string     `codec:"extra_labels"`
	ImagePullTimeout     string       `codec:"image_pull_timeout"`
	GC                   GCConfig     `codec:"gc"`
	Volumes              VolumeConfig `codec:"volumes"`
	Auth                 *AuthConfig  `codec:"auth"`
}

// GCConfig controls garbage-collection behaviour.
type GCConfig struct {
	// Container removes the container when the Nomad task exits.
	Container bool `codec:"container"`
}

// VolumeConfig controls host volume mounting permissions.
type VolumeConfig struct {
	// Enabled permits tasks to use host bind-mounts via the volumes config key.
	Enabled bool `codec:"enabled"`
}

// AuthConfig holds OCI registry authentication credentials.
type AuthConfig struct {
	Username      string `codec:"username"`
	Password      string `codec:"password"`
	ServerAddress string `codec:"server_address"`
}

// TaskConfig holds the per-task driver configuration decoded from the task's
// config{} block in the Nomad job specification.
type TaskConfig struct {
	Image            string            `codec:"image"`
	Command          string            `codec:"command"`
	Args             []string          `codec:"args"`
	Entrypoint       string            `codec:"entrypoint"`
	WorkingDir       string            `codec:"working_dir"`
	Env              map[string]string `codec:"env"`
	Volumes          []string          `codec:"volumes"`
	Tmpfs            []string          `codec:"tmpfs"`
	Ports            []string          `codec:"ports"`
	NetworkMode      string            `codec:"network_mode"`
	Hostname         string            `codec:"hostname"`
	User             string            `codec:"user"`
	Labels           map[string]string `codec:"labels"`
	CapAdd           []string          `codec:"cap_add"`
	CapDrop          []string          `codec:"cap_drop"`
	ReadonlyRootfs   bool              `codec:"readonly_rootfs"`
	Init             bool              `codec:"init"`
	TTY              bool              `codec:"tty"`
	DNS              []string          `codec:"dns"`
	DNSSearch        []string          `codec:"dns_search"`
	DNSOptions       []string          `codec:"dns_options"`
	Rosetta          bool              `codec:"rosetta"`
	ForcePull        bool              `codec:"force_pull"`
	ImagePullTimeout string            `codec:"image_pull_timeout"`
	Auth             *AuthConfig       `codec:"auth"`
	SSHAgent         bool              `codec:"ssh_agent"`
	Virtualization   bool              `codec:"virtualization"`
}

// inspectState represents the container status inside a container inspect
// JSON response.
//
// NOTE: Field names are best-guess mappings against Apple's container CLI
// output. Adjust to match the actual `container inspect` JSON schema once
// confirmed against a running system.
type inspectState struct {
	Status   string `json:"status"` // "running", "stopped", "exited"
	ExitCode int    `json:"exitCode"`
	Pid      int    `json:"pid"`
}

// inspectData is the top-level object returned by `container inspect <name>`.
type inspectData struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	State inspectState `json:"state"`
	Image string       `json:"image"`
}

// statsData is an element in the JSON array returned by
// `container stats --no-stream --format json`.
//
// NOTE: Field names are best-guess mappings. Adjust once confirmed against a
// running system.
type statsData struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemUsage    uint64  `json:"mem_usage"`
	MemLimit    uint64  `json:"mem_limit"`
	NetInput    uint64  `json:"net_input"`
	NetOutput   uint64  `json:"net_output"`
	BlockInput  uint64  `json:"block_input"`
	BlockOutput uint64  `json:"block_output"`
	PIDs        uint64  `json:"pids"`
}

// versionOutput is (a subset of) the JSON returned by
// `container system version --format json`.
type versionOutput struct {
	Version   string `json:"version"`
	AppName   string `json:"appName"`
	BuildType string `json:"buildType"`
	Commit    string `json:"commit"`
}

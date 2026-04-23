# nomad-driver-container

A [Nomad](https://www.nomadproject.io/) task driver plugin that runs Linux
containers as lightweight virtual machines on macOS using Apple's
[container](https://github.com/apple/container) platform.

Each container gets its own dedicated micro-VM backed by Apple's
Virtualization framework, providing VM-level isolation with near-native
performance on Apple Silicon.

---

## Features

### Core container lifecycle
- **Start / stop / destroy** containers via `container run`, `container stop`,
  and `container delete`
- **Detached execution** — containers are launched with `--detach` and tracked
  by name across driver restarts
- **Task recovery** — the driver reattaches to running containers after a Nomad
  client or plugin process restart without interrupting workloads

### Image management
- **OCI-compatible images** — pull from Docker Hub, GHCR, ECR, or any standard
  registry
- **Force pull** — always fetch the latest image digest before starting
- **Per-task registry auth** — supply credentials directly in the task config
- **Plugin-level default auth** — configure a fallback credential for all tasks
- **Configurable pull timeout** — set globally or per task

### Resource management
- **Memory limits** mapped from Nomad's `resources.memory` block
  (`--memory <n>MiB`)
- **CPU core reservation** mapped from Nomad's `resources.cores` field
  (`--cpus <n>`)
- **Resource statistics** — CPU %, memory usage/limit, network I/O, and block
  I/O reported to Nomad via `container stats`

### Networking (macOS 26+)
- **Port publishing** driven by Nomad's `network { port … }` block; dynamic
  host ports are automatically wired to container ports (`-p host:container`)
- **Named user-defined networks** using the `network_mode` task config key
- **Shared network namespaces** between tasks in the same group with
  `network_mode = "task:<othertask>"` — enables sidecar patterns without
  exposing internal ports
- **Host networking** (`network_mode = "host"`)
- **Isolated networking** (`network_mode = "none"`)
- **Custom DNS** servers, search domains, and resolver options

### Storage
- **Bind-mount volumes** (`volumes = ["host:container:options"]`) — operator
  controlled via the `volumes.enabled` plugin config flag
- **Nomad alloc directory mounts** (`/alloc`, `/local`, `/secrets`) injected
  automatically through Nomad's standard mount mechanism
- **tmpfs mounts** at arbitrary container paths
- **Read-only root filesystem** (`readonly_rootfs = true`)

### Security
- **Linux capability tuning** — add (`cap_add`) or drop (`cap_drop`) individual
  capabilities
- **Custom user/UID** for the container process (`user` field)
- **Per-VM isolation** — each container runs in its own lightweight VM, removing
  the shared-kernel attack surface present in traditional container runtimes

### Logging
- **Nomad-native log collection** — `container logs --follow` is piped directly
  into Nomad's stdout FIFO for rotation-free, zero-config log handling
- **Log collection disable** — set `disable_log_collection = true` in the
  plugin config to skip Nomad log capture when you are using an external
  aggregator

### Signals & exec
- **Signal delivery** — send any POSIX signal to the container's main process
  with `nomad alloc signal` (backed by `container kill --signal`)
- **In-container exec** — run arbitrary commands inside a running container with
  `nomad alloc exec` (backed by `container exec`)

### Process management
- **Init process** (`init = true`) — runs a minimal PID-1 that forwards
  signals and reaps zombie processes
- **TTY allocation** (`tty = true`) for interactive workloads

### macOS / Apple Silicon specific
- **Rosetta 2 translation** (`rosetta = true`) — run x86_64 Linux binaries on
  Apple Silicon with transparent instruction translation, no cross-compilation
  required
- **SSH agent forwarding** (`ssh_agent = true`) — inject the host SSH agent
  socket into the container
- **Nested virtualisation** (`virtualization = true`) — expose hardware
  virtualisation capabilities to the container guest (requires host + guest
  support)
- **Per-VM memory** — memory is allocated per container VM; the `--memory` flag
  sets an upper bound; actual RSS only grows as the workload demands it

### Operator configuration
- **Metadata labels** — automatically stamp containers with Nomad job/task
  metadata via `extra_labels` (glob patterns supported)
- **Garbage collection** toggle — keep or remove containers after task exit
  (`gc.container`)

---

## Requirements

| Requirement | Version |
|---|---|
| macOS | 26 (Sequoia) or later |
| Hardware | Apple Silicon (M1 / M2 / M3 / M4 series) |
| Apple container CLI | ≥ 0.11.0 |
| Nomad | ≥ 1.8.0 |
| Go (to build) | ≥ 1.22 |

Install the Apple container CLI from the
[releases page](https://github.com/apple/container/releases) and start the
system service:

```sh
container system start
```

---

## Building from source

```sh
git clone https://github.com/hashicorp/nomad-driver-container
cd nomad-driver-container

# Resolve dependencies
go mod tidy

# Build the plugin binary
make dev

# The binary is placed at ./build/nomad-driver-container
```

> **Note** — `github.com/hashicorp/nomad` may require additional `replace`
> directives in `go.mod`.  Consult Nomad's own
> [`go.mod`](https://github.com/hashicorp/nomad/blob/main/go.mod) for the
> complete list of replace stanzas needed for the version you are targeting.

---

## Installation

### Automated

[**Nomadintosh**](https://github.com/anultravioletaurora/Nomadintosh) provides
a fully automated install for macOS using Homebrew and Ansible. It installs
Nomad, the Apple container CLI, and this driver in one step — the quickest way
to get a working cluster on Apple Silicon.

### Manual

1. Build or download the `nomad-driver-container` binary.
2. Place it in the Nomad `plugin_dir` on each macOS client node.
3. Add the plugin configuration block to your Nomad client config (see below).
4. Restart the Nomad client.

---

## Driver configuration

Configure the plugin in the Nomad client agent HCL file.

```hcl
plugin "nomad-driver-container" {
  config {
    # Path to the Apple container CLI binary.
    container_path = "/usr/local/bin/container"

    # Remove containers from the system when their task exits.
    gc {
      container = true
    }

    # Allow tasks to bind-mount host paths into containers.
    volumes {
      enabled = true
    }

    # Automatically append these Nomad metadata labels to every container.
    # Supports glob patterns such as "task*".
    # Possible values: job_name, job_id, task_group_name, task_name,
    #                  namespace, node_name, node_id
    extra_labels = ["job_name", "task_group_name", "task_name"]

    # Maximum time to wait for an image pull before failing the task.
    image_pull_timeout = "5m"

    # Set to true to opt out of Nomad log collection for all container tasks.
    disable_log_collection = false

    # Optional default registry credentials (overridden per-task by auth {}).
    # auth {
    #   username       = "myuser"
    #   password       = "s3cr3t"
    #   server_address = "registry.example.com"
    # }
  }
}
```

---

## Task configuration

| Key | Type | Default | Description |
|---|---|---|---|
| `image` | string | **required** | OCI image reference (e.g. `nginx:latest`) |
| `command` | string | `""` | Override the image's CMD |
| `args` | list(string) | `[]` | Arguments passed to the container's entry process |
| `entrypoint` | string | `""` | Override the image's ENTRYPOINT |
| `working_dir` | string | `""` | Working directory inside the container |
| `env` | map(string) | `{}` | Extra environment variables (Nomad runtime vars are always injected) |
| `volumes` | list(string) | `[]` | `host:container[:options]` bind-mount specs |
| `tmpfs` | list(string) | `[]` | Container paths to mount as tmpfs |
| `ports` | list(string) | `[]` | Port labels from the group `network` block to publish |
| `network_mode` | string | `""` | Network mode: `default`, `host`, `none`, `task:<name>`, or a named network |
| `hostname` | string | `""` | Container hostname |
| `user` | string | `""` | `name\|uid[:gid]` for the container process |
| `labels` | map(string) | `{}` | Key/value metadata labels on the container |
| `cap_add` | list(string) | `[]` | Linux capabilities to add (e.g. `"NET_ADMIN"`) |
| `cap_drop` | list(string) | `[]` | Linux capabilities to drop |
| `readonly_rootfs` | bool | `false` | Mount the root filesystem read-only |
| `init` | bool | `false` | Run a minimal init (PID 1) that reaps zombies and forwards signals |
| `tty` | bool | `false` | Allocate a pseudo-TTY |
| `dns` | list(string) | `[]` | Custom DNS server addresses |
| `dns_search` | list(string) | `[]` | DNS search domains |
| `dns_options` | list(string) | `[]` | DNS resolver options (e.g. `"ndots:5"`) |
| `rosetta` | bool | `false` | Enable Rosetta 2 x86_64 translation (Apple Silicon only) |
| `force_pull` | bool | `false` | Always pull the image before starting |
| `image_pull_timeout` | string | plugin default | Per-task pull timeout (e.g. `"10m"`) |
| `ssh_agent` | bool | `false` | Forward the host SSH agent socket into the container |
| `virtualization` | bool | `false` | Expose nested virtualisation capabilities |
| `auth` | block | — | Per-task registry credentials; see below |

### `auth` block

```hcl
auth {
  username       = "myuser"
  password       = "s3cr3t"
  server_address = "registry.example.com"  # optional
}
```

---

## Example jobs

### Nginx web server

```hcl
job "nginx" {
  datacenters = ["dc1"]
  type        = "service"

  group "web" {
    network {
      port "http" { to = 80 }
    }

    task "nginx" {
      driver = "container"

      config {
        image = "nginx:latest"
        ports = ["http"]
        init  = true

        labels = {
          "app" = "nginx"
        }
      }

      resources {
        cpu    = 200
        memory = 128
      }
    }
  }
}
```

### Sidecar / shared network namespace (Redis + exporter)

See [`examples/jobs/redis.nomad`](examples/jobs/redis.nomad) for a two-task
setup where a Prometheus exporter joins the Redis task's network namespace via
`network_mode = "task:redis"`.

### Rosetta 2 — running x86_64 images on Apple Silicon

```hcl
task "legacy-app" {
  driver = "container"

  config {
    image   = "amd64/ubuntu:22.04"
    command = "/usr/bin/my-x86-binary"
    rosetta = true   # transparent x86_64 → ARM64 translation
  }
}
```

See [`examples/jobs/rosetta.nomad`](examples/jobs/rosetta.nomad) for the full
example.

---

## Local development

```sh
# 1. Build the plugin
make dev

# 2. Copy the binary into the plugin directory
mkdir -p /tmp/nomad-container-plugins
cp ./build/nomad-driver-container /tmp/nomad-container-plugins/

# 3. Start the Nomad server (in one terminal)
nomad agent -config=./examples/nomad/server.hcl 2>&1 | tee server.log &

# 4. Start the Nomad client (in another terminal — requires sudo for cgroups)
sudo nomad agent -config=./examples/nomad/client.hcl 2>&1 | tee client.log

# 5. Run an example job
nomad job run examples/jobs/nginx.nomad

# 6. View logs
nomad alloc logs <ALLOC_ID>

# 7. Exec into the running container
nomad alloc exec <ALLOC_ID> /bin/sh
```

---

## Known limitations

| Limitation | Notes |
|---|---|
| **macOS 26+ required** | The Apple container platform targets macOS 26; it will not run on macOS 15 without functional gaps. |
| **Apple Silicon only** | The underlying Virtualization framework features used by `container` require Apple Silicon. |
| **Combined log stream** | `container logs` does not separate stdout and stderr; the driver forwards the combined stream to Nomad's stdout FIFO. |
| **No device passthrough** | Each container runs in its own VM; `/dev/xxx` device passthrough is not currently supported by this driver. |
| **Inspect / stats JSON schema** | The exact JSON field names emitted by `container inspect` and `container stats --format json` should be verified against the version of `container` you deploy. See the `TODO` comments in `config.go`. |
| **No `--log-driver` equivalent** | Apple's `container` does not expose pluggable log drivers; all log output goes through `container logs`. |

---

## Comparison with docker / podman drivers

| Feature | docker | podman | **container** (this driver) |
|---|---|---|---|
| OCI image support | ✅ | ✅ | ✅ |
| Port publishing | ✅ | ✅ | ✅ |
| Volume mounts | ✅ | ✅ | ✅ |
| Environment variables | ✅ | ✅ | ✅ |
| Resource limits | ✅ | ✅ | ✅ |
| Signal delivery | ✅ | ✅ | ✅ |
| Exec into container | ✅ | ✅ | ✅ |
| Init process | ✅ | ✅ | ✅ |
| Shared network namespace | ✅ | ✅ | ✅ |
| Nomad log collection | ✅ | ✅ | ✅ |
| Pluggable log drivers | ✅ | ✅ | ❌ |
| SELinux options | ❌ | ✅ | ❌ |
| AppArmor profiles | ✅ | ✅ | ❌ |
| Per-container VM isolation | ❌ | ❌ | ✅ |
| Rosetta 2 (x86 on ARM) | ❌ | ❌ | ✅ |
| SSH agent forwarding | ❌ | ❌ | ✅ |
| Linux host support | ✅ | ✅ | ❌ |
| macOS 26+ support | via VM | via VM | ✅ native |

---

## License

[MPL-2.0](LICENSE)

# Redis example with a sidecar Prometheus exporter.
#
# Every container runs in its own VM with its own network stack, so the two
# tasks can't share a network namespace. The exporter reaches Redis through
# the port Redis publishes on the host.
#
# Run with:
#   nomad job run examples/jobs/redis.nomad

job "redis" {
  datacenters = ["dc1"]
  type        = "service"

  group "cache" {
    count = 1

    network {
      port "redis"    { to = 6379 }
      port "exporter" { to = 9121 }
    }

    # ── Primary workload ──────────────────────────────────────────────────────

    task "redis" {
      driver = "container"

      config {
        image = "redis:7-alpine"
        ports = ["redis"]

        # Run an init process for clean signal handling.
        init = true

        labels {
          app       = "redis"
          component = "cache"
        }
      }

      resources {
        cpu    = 500
        memory = 256
      }

      service {
        name     = "redis"
        port     = "redis"
        provider = "nomad"
      }
    }

    # ── Sidecar: Prometheus Redis exporter ────────────────────────────────────

    task "redis-exporter" {
      driver = "container"

      # This task starts after the redis task is running.
      lifecycle {
        hook    = "poststart"
        sidecar = true
      }

      config {
        image = "oliver006/redis_exporter:latest"
        ports = ["exporter"]

        env {
          REDIS_ADDR = "redis://${NOMAD_ADDR_redis}"
        }
      }

      resources {
        cpu    = 100
        memory = 64
      }

      service {
        name     = "redis-exporter"
        port     = "exporter"
        provider = "nomad"
      }
    }
  }
}

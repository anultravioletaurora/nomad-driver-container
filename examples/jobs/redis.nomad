# Redis example with a sidecar exporter demonstrating shared networking.
#
# Both tasks join the same network namespace: the redis task creates the
# namespace and the exporter task joins it via network_mode = "task:redis".
# This lets the exporter reach Redis on 127.0.0.1 without exposing the
# stats port externally.
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

        labels = {
          "app"       = "redis"
          "component" = "cache"
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

        # Join the network namespace of the redis task so we can reach
        # Redis on 127.0.0.1:6379 without an external port.
        network_mode = "task:redis"

        env = {
          REDIS_ADDR = "redis://127.0.0.1:6379"
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

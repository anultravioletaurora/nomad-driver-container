# Nginx web server example using nomad-driver-container.
#
# Run with:
#   nomad job run examples/jobs/nginx.nomad
#
# Verify:
#   nomad job status nginx
#   curl http://localhost:8080

job "nginx" {
  datacenters = ["dc1"]
  type        = "service"

  group "web" {
    count = 1

    network {
      port "http" { to = 80 }
    }

    task "nginx" {
      driver = "container"

      config {
        # OCI image reference – any registry understood by `container image pull`
        image = "nginx:latest"

        # Publish the Nomad-allocated port to container port 80.
        ports = ["http"]

        # Inject environment variables into the container.
        env = {
          NGINX_HOST = "localhost"
          NGINX_PORT = "80"
        }

        # Add runtime metadata labels.
        labels = {
          "app"     = "nginx"
          "env"     = "dev"
        }

        # Run a minimal init process to forward signals and reap zombies.
        init = true

        # Mount a custom nginx config from the Nomad alloc directory.
        # volumes = [
        #   "local/nginx.conf:/etc/nginx/nginx.conf:ro",
        # ]

        # Capabilities: drop all unused capabilities for least-privilege.
        cap_drop = ["ALL"]
        cap_add  = ["CHOWN", "SETUID", "SETGID", "NET_BIND_SERVICE"]

        # Enable Rosetta 2 to run x86_64 images on Apple Silicon.
        # rosetta = true
      }

      resources {
        cpu    = 1   # MHz
        memory = 128   # MiB
      }

      service {
        name     = "nginx"
        port     = "http"
        provider = "nomad"

        check {
          type     = "http"
          path     = "/"
          interval = "10s"
          timeout  = "2s"
        }
      }
    }
  }
}

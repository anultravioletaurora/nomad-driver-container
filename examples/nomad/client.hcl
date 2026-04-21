# Nomad client configuration for local development with nomad-driver-container.
#
# Start with:
#   sudo nomad agent -config=./examples/nomad/client.hcl 2>&1 | tee client.log

data_dir  = "/tmp/nomad-container-client"
log_level = "DEBUG"

client {
  enabled = true

  # The plugin_dir must contain the compiled nomad-driver-container binary.
  # Build it first with:  make dev && cp ./build/nomad-driver-container <plugin_dir>
  plugin_dir = "/tmp/nomad-container-plugins"
}

plugin "nomad-driver-container" {
  config {
    # Path to the Apple container CLI.  Default: /usr/local/bin/container
    container_path = "/usr/local/bin/container"

    # Remove containers from the system when their Nomad tasks exit.
    gc {
      container = true
    }

    # Allow tasks to bind-mount host directories into containers.
    volumes {
      enabled = true
    }

    # Automatically add these Nomad metadata labels to every container.
    extra_labels = ["job_name", "task_group_name", "task_name", "namespace"]

    # Maximum time to wait for an image pull.
    image_pull_timeout = "5m"

    # Set to true to disable Nomad's built-in log collection for this driver.
    # You would use this if you ship logs via an external aggregator instead.
    disable_log_collection = false
  }
}

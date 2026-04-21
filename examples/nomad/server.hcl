# Minimal Nomad server configuration for local development.
#
# Start with:
#   nomad agent -config=./examples/nomad/server.hcl 2>&1 | tee server.log

data_dir  = "/tmp/nomad-container-server"
log_level = "DEBUG"

server {
  enabled          = true
  bootstrap_expect = 1
}

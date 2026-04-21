# Rosetta example: run an x86_64 image on Apple Silicon with Rosetta 2.
#
# This demonstrates the macOS-specific `rosetta = true` flag which enables
# transparent x86_64 → ARM64 binary translation inside the VM.
#
# Requirements:
#   - Apple Silicon Mac (M1/M2/M3/M4 series)
#   - macOS 26+  with Rosetta 2 installed
#   - Apple container CLI ≥ 0.11.0

job "rosetta-demo" {
  datacenters = ["dc1"]
  type        = "batch"

  group "demo" {
    task "uname" {
      driver = "container"

      config {
        # An x86_64-only image – on Apple Silicon this would fail without Rosetta.
        image   = "amd64/alpine:latest"
        command = "/bin/sh"
        args    = ["-c", "uname -m && echo 'Rosetta translation is working!'"]

        # Enable Rosetta 2 translation inside the lightweight VM.
        rosetta = true
      }

      resources {
        cpu    = 100
        memory = 64
      }
    }
  }
}

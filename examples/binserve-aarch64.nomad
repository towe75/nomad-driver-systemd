job "binserv-aarch64" {

  datacenters = ["dc1"]
  type        = "service"

  group "binserv" {

    task "binserv" {

      driver = "systemd"
      kill_timeout = "5s"

      artifact {
        source      = "https://github.com/mufeedvh/binserve/releases/download/v0.2.0/binserve-v0.2.0-aarch64-unknown-linux-gnu.tar.gz"
          options {
    checksum = "sha256:fd5453bf734add72e0ae62a64e207b0c4e69060669d170240b4f6cfadc0e6e7e"
  }
      }

      config {
        command = "binserve-v0.2.0-aarch64-unknown-linux-gnu/binserve"
        args = [
          "--host",
          "0.0.0.0:1337",
        ]
      }

      resources {
        cpu    = 200
        memory = 20
      }

    }
  }
}


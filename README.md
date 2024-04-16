Nomad systemd Driver
=====================

This is  a nomad task driver which leverages systemd transient units as isolation
primitive. 

### Why?

TL;DR : Curiosity

This project tries to combine a few ideas:

- orchestrate, configure and deploy service definitions via nomad
- use nomad features to download your binary or image (artifact stanza etc.)
- run the actual binary via the prevalent systemd service manager
- do not delegate service restarts, lifecycle to systemd, use nomad

But we have already [nomad exec and raw-exec](https://www.nomadproject.io/docs/drivers/exec) drivers!
Yes, they are at first sight somewhat similar but not very feature rich and/or insecure.

Other people played also with nomad and systemd:
- [arianvpn/monad-driver-systemd](https://github.com/arianvp/nomad-driver-systemd)
- [nspawn driver](https://www.nomadproject.io/plugins/drivers/community/nspawn)

#### Systemd usage

Having systemd between nomand and the actual service binary brings certain benefits:

- stability through delegation, the nomad agent is not responsible for controlling the actual command
- this plugin talks to systemd via dbus. That means that we have the possibility to run
  nomad as unprivileged user or even in a container if we configure polkit correctly. 
- leverage systemd features

About transient units: a lesser known systemd feature
(see [systemd-run](https://www.freedesktop.org/software/systemd/man/systemd-run.html)) allows you to
run a unit ad-hoc without declaring it in a file and without performing a daemon-reload etc.
Systemd will forget about this unit once it completes. 

The plugin builds upon this mechanism and manages systemd units via the dbus api.
It's easily possible to mirror almost all systemd-exec unit options into the task driver.

One exception is important: service failures and restarts must not be done by systemd because
it would heavily conflit with nomads scheduler.

#### Containerless

Containers are nice. But they do not come for free. 

Sometimes we want to run a simple (go, rust,...) binary on our worker nodes.
Wrapping such a binary into a container image is of course possible. 

But do we really have to do it?

We had services long before we had containers. A service was usually installed by a package manager and
started/supervised by the hosts init system (sysV, supervisor, systemd, ...).

Systemd added many cgroup based security features to services which most people would 
associate with a container runtime:

- resource limits and accounting (memory, cpu, io, ...)
- filesystem isolation
- ephemeral uids
- even network isolation

What about images?

Images are nice. It's the defacto standard to bundle and distribute all dependencies for a service.
But as sayed above: sometimes your service is just a binary and it's configuration file. 

In this case a mere fileserver (think S3 bucket...) can replace a image repository.

#### maybe: A thin wasm driver

Some people move towards web assembly (WASM) on the server. 
They need a way to distribute and start a WASM binary without much overhead.

It's certainly possible to evolve this plugin also into this direction.

We could store .wasm files on https servers, use [nomads artifact](https://www.nomadproject.io/docs/job-specification/artifact) feature to download them directly into 
the allocation directory and finally launch a service. 

## Features

It's a rather simple POC for now. 
My first goal is to see if people find it useful.

Only a few features are implemented:

* [ ] Run in a systemd user session. *nomad must be root for now!*
* [x] Specify a command and arguments
* [x] Run that command under a different Uid
* [ ] Run that command with a different Gid
* [x] [Nomad runtime environment](https://www.nomadproject.io/docs/runtime/environment.html) is populated
* [ ] Send signals via nomad
* [ ] Nomad exec support
* [x] Configure environment variables
* [x] Collect basic CPU and Memory metrics
* [x] Optionally forward logs to nomad instead of system Journal
* [x] Propagate command exit code back into nomad
* [x] Propagate command OOM kills back into nomad
* [ ] Configure CPU, resource limits
* [x] Configure Memory resource limits
* [ ] Configure Swap resource limits
* [ ] Configure IO resource limits
* [ ] Configure systemd based filesystem restrictions, e.g. chroot
* [ ] Make use of systemd image mounts
* [ ] Bind mount host volumes etc 
* [ ] Make use of the many systemd security options
* [ ] ...

## Requirements

- [Go](https://golang.org/doc/install) v1.18 or later (to compile the plugin)
- [Nomad](https://www.nomadproject.io/downloads.html) to run the plugin
- A linux system or container controlled by systemd

## Building The Driver from source

```sh
$ make build
```

## Driver Configuration

No configuration is possible yet.

## Task Configuration

* **command** - (Manatory) The command to run in your task.

The exact behavior depends on the fact if you configure a absolute (first char is a "/") or
relative path.

A command with a relative path will be prefixed with the local task directory. This makes
it convenient to download and start a binary via  and [artifact](https://www.nomadproject.io/docs/job-specification/artifact). 
stanza.

```hcl
config {
  command = "some-command"
}
```

* **args** - (Optional) A list of arguments to the command. 

```hcl
config {
  args = [
    "arg1",
    "arg2",
  ]
}
```

* **user** - Run the command as a specific user/uid. See [Task configuration](https://www.nomadproject.io/docs/job-specification/task.html#user)

```hcl
user = nobody

config {
}

```

* **logging** - Configure logging. 

`driver = "nomad"` (default) Systemd redirects its stdout and stderr streams directly to Nomad.

```hcl
config {
  logging = {
    driver = "nomad"
  }
}
```

`driver = "journald"` Write logs into the system journal. Nomad will not receive any logs.

```hcl
config {
  logging = {
    driver = "journald"
  }
}
```


% fakemachine(1)

# NAME

fakemachine - fake a machine


Creates a virtual machine based on the currently running system.

# SYNOPSIS

```
fakemachine [options] <command to run inside machine>
fakemachine [--help]
```

Application Options:
```
  -b, --backend=[auto|kvm|uml|qemu] Virtualisation backend to use (default: auto)
  -v, --volume=                     volume to mount
  -i, --image=                      image to add
  -e, --environ-var=                Environment variables (use -e VARIABLE:VALUE syntax)
  -m, --memory=                     Amount of memory for the fakemachine in megabytes
  -c, --cpus=                       Number of CPUs for the fakemachine
  -s, --scratchsize=                On-disk scratch space size (with a unit suffix, e.g. 4G); if unset,
                                    memory backed scratch space is used
      --show-boot                   Show boot/console messages from the fakemachine

Help Options:
  -h, --help                        Show this help message
```

# INSTALLATION

```
$ export GOPATH=~/go
$ export PATH=$PATH:~/go/bin
$ go install github.com/go-debos/fakemachine/cmd/fakemachine@latest
```

# USAGE

```
$ fakemachine echo test
Running echo test using kvm backend
test
```

# DOCKER CONTAINER

fakemachine is also available as a container image on [ghcr.io](https://github.com/go-debos/debos/pkgs/container/fakemachine).

To run it:

```
$ docker pull ghcr.io/go-debos/fakemachine:main

$ docker run \
  --rm \
  --interactive \
  --tty \
  --device /dev/kvm \
  --user $(id -u) \
  --workdir /work \
  --mount "type=bind,source=$(pwd),destination=/work" \
  --security-opt label=disable \
  ghcr.io/go-debos/fakemachine:main \
  echo test

Running echo test using kvm backend
test
```

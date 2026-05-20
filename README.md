# fakemachine - fake a machine

fakemachine creates a virtual machine derived from the current root filesystem
and allows the user to run commands inside it.

It is provided as a standalone executable as well as a Go library and is
primarily used by [debos](https://github.com/go-debos/debos) to execute image
build actions inside a VM. This gives stronger isolation than chroot and allows
unprivileged users to perform operations that would normally require root on the
host, such as mounting filesystem images.

If fakemachine is ran inside a container, the virtual machine's root filesystem
is derived from the container's root filesystem.

## Synopsis

```
fakemachine [options] <command to run inside machine>
fakemachine [--help]
```

Application Options:
```
  -b, --backend=[auto|kvm|qemu] Virtualisation backend to use (default: auto)
  -v, --volume=                 volume to mount
  -i, --image=                  image to add
  -e, --environ-var=            Environment variables (use -e VARIABLE:VALUE syntax)
  -m, --memory=                 Amount of memory for the fakemachine in megabytes
  -c, --cpus=                   Number of CPUs for the fakemachine
  -S, --sectorsize=             Override image sector size
  -s, --scratchsize=            On-disk scratch space size (with a unit suffix, e.g. 4G); if unset, memory backed scratch space is used
      --show-boot               Show boot/console messages from the fakemachine
  -q, --quiet                   Don't show logs from fakemachine or the backend; only print the command's stdout/stderr
      --version                 Print fakemachine version

Help Options:
  -h, --help                    Show this help message
```

## Installation

```
$ export GOPATH=~/go
$ export PATH=$PATH:~/go/bin
$ go install github.com/go-debos/fakemachine/cmd/fakemachine@latest
```

## Usage

```
$ fakemachine echo test
Running echo test using kvm backend
test
```

## Docker container

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

## Contributing

To contribute to fakemachine, see the dedicated [contributing documentation](https://github.com/go-debos/fakemachine/blob/main/CONTRIBUTING.md).

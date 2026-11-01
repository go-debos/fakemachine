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
  -o, --backend-option=         Backend specific options (use -o KEY:VALUE syntax)
  -v, --volume=                 volume to mount
  -i, --image=                  image to add
  -e, --environ-var=            Environment variables (use -e VARIABLE:VALUE syntax)
  -m, --memory=                 Amount of memory for the fakemachine (parsed with human-readable suffix; assumed bytes if no suffix) (default: 2Gb)
  -c, --cpus=                   Number of CPUs for the fakemachine (defaults to number of CPUs on the host)
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

## Backend options

Backend specific options can be passed with `--backend-option KEY:VALUE`.
Unknown options are rejected.

The `qemu` and `kvm` backends support the following options:

- `kernel-path`: path to the kernel image to boot instead of the host kernel.
- `modules-path`: path to the modules directory of the kernel to boot, e.g.
  `<root>/lib/modules/<kernel release>` as installed by `make modules_install`.
  The modules required to boot are copied into the initrd from this directory;
  it must contain `modules.dep` and its name must match the release of the
  kernel being booted. If unset and `kernel-path` is set, the directory
  containing the kernel image is used; if that directory does not contain
  `modules.dep`, e.g. in a kernel build tree, no modules are added to the
  initrd and the required drivers must be built into the kernel. This is
  checked before booting against the `modules.builtin` file found in the
  directory containing the kernel image or one of its parents, as generated
  at the root of a kernel build tree.

If both are unset, the kernel and modules of the host are used.

```
$ fakemachine \
  --backend-option kernel-path:/path/to/Image \
  --backend-option modules-path:/path/to/lib/modules/6.12.0 \
  echo test
$ fakemachine \
  --backend-option kernel-path:/path/to/lib/modules/6.12.0/vmlinuz \
  echo test
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

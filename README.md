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
      --print-backend-options   Print the default and used values of the backend specific options before running
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

Backend specific options can be passed with `-o, --backend-option KEY:VALUE`.
Unknown options are rejected. `--print-backend-options` prints the default
value of every option supported by the backend and the value resolved by the
backend, e.g. the path of the host kernel when `kernel-path` is unset, before
starting the backend.

The `qemu` and `kvm` backends support the following options:

- `kernel-path`: path to the kernel image to boot instead of the host kernel.
  EFI zboot images (`CONFIG_EFI_ZBOOT`) compressed with gzip, xz or zstd are
  decompressed before booting, as qemu cannot boot them directly.
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

## Kernel requirements

The `qemu` and `kvm` backends need the kernel to boot to support the virtual
hardware and filesystems used by fakemachine, in addition to the
[requirements of systemd](https://github.com/systemd/systemd/blob/main/README).
By default the currently running kernel is used, or the latest kernel in
`/lib/modules` if the modules of the running kernel are not installed, e.g.
in a container. Its modules are taken from `/usr/lib/modules/<release>`, or
`/lib/modules/<release>` on systems without merged `/usr`, and the kernel
image is `vmlinuz` within that directory or else `/boot/vmlinuz-<release>`.
Distribution kernels usually support all of these. The mainline x86_64
defconfig builds all of them in, while the arm64 defconfig builds
`CONFIG_NET_9P`, `CONFIG_NET_9P_VIRTIO` and `CONFIG_9P_FS` as modules, so
booting an arm64 kernel straight from a build tree requires them to be built
in. A custom kernel needs the following options:

- `CONFIG_BLK_DEV_INITRD`: boot from the initramfs created by fakemachine.
- `CONFIG_VIRTIO_PCI`: virtio devices, plus `CONFIG_PCI_HOST_GENERIC` on arm64.
- `CONFIG_VIRTIO_CONSOLE`: console used for the command output.
- `CONFIG_NET_9P`, `CONFIG_NET_9P_VIRTIO` and `CONFIG_9P_FS`: volumes shared
  with the host, including the root filesystem.
- `CONFIG_VIRTIO_BLK`: disk images and on-disk scratch space.
- `CONFIG_EXT4_FS`: on-disk scratch space.
- `CONFIG_VIRTIO_NET`: networking.
- `CONFIG_SERIAL_8250_CONSOLE` on amd64 or `CONFIG_SERIAL_AMBA_PL011_CONSOLE`
  on arm64: boot console.

`CONFIG_VIRTIO_PCI`, `CONFIG_VIRTIO_CONSOLE`, `CONFIG_NET_9P`,
`CONFIG_NET_9P_VIRTIO` and `CONFIG_9P_FS` may be built as modules, which
are copied into the initramfs; the modules directory must then contain the
`modules.builtin`, `modules.alias` and `modules.symbols` files generated by
`depmod`. The other options are needed after the root filesystem is mounted
from the host, so they should be built in when the modules of the kernel to
boot are not installed on the host, for instance when using the
`modules-path` backend option.

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

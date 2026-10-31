# The unshare backend

The `unshare` backend runs the command directly on the host kernel, inside new
Linux user, mount, PID, UTS and IPC namespaces. It does not boot a VM, kernel or
initrd, so commands run at close to native speed and the backend needs neither
`/dev/kvm` nor host root.

It has to be selected explicitly with `--backend=unshare`. The `auto` backend
never picks it, because it cannot provide disk images (see below).

## Requirements

- The `unshare` and `pivot_root` binaries from util-linux (2.38 or later for
  `--map-auto`). On Debian and Ubuntu `pivot_root` is in the
  `util-linux-extra` package.
- The `hostname` binary (the `inetutils` package on Arch, `hostname` on
  Debian, Ubuntu and Fedora). Without it the machine's hostname is only set
  when fakemachine runs as root.
- For ownership other than root (needed by `tar`, `dpkg` and `debootstrap`):
  `newuidmap`/`newgidmap` (the `uidmap` package) and `/etc/subuid` and
  `/etc/subgid` entries for the invoking user. Without these the backend falls
  back to mapping only root and prints a warning. When fakemachine already runs
  as root, for example in a CI container, it first tries
  `--map-users=all --map-groups=all` (util-linux 2.40 or later), which maps the
  whole id range through unchanged and needs no subordinate ids.
- Unprivileged user namespaces enabled on the host. A host can disable them in
  three ways:
  - `kernel.unprivileged_userns_clone=0` (Debian kernels);
  - AppArmor's `kernel.apparmor_restrict_unprivileged_userns=1` (Ubuntu);
  - `user.max_user_namespaces=0`.
- In a container, the default Docker/Podman seccomp and AppArmor profiles block
  `CLONE_NEWUSER`. The masked `/proc` paths also prevent a fresh procfs mount.
  CI therefore runs this backend with `--privileged`. Running it
  inside a privileged container is fine because the namespaces are created
  inside it.

`Supported()` probes the namespace setup with `unshare ... true`, first with
subordinate id mapping and then without it, and reports the failures if neither
works.

## How it works

`Machine.startup` detects that the backend implements `hostKernelBackend` and
calls `Prepare()` instead of setting up the scratch disk and building the initrd.
`Prepare()` writes `<tmpdir>/setup.sh`, which `Start()` runs as PID 1 of the new
namespaces:

```
unshare --user --mount --propagation private \
        --pid --fork --kill-child --uts --ipc \
        --map-auto --map-root-user /bin/sh setup.sh
```

The script does the following:

1. Mounts a tmpfs root and creates the directory skeleton, including the
   merged-usr symlinks where applicable.
2. Copies the same small set of `/etc` files that the initrd gets: `passwd`,
   `group`, `nsswitch.conf`, `ld.so.*` and `resolv.conf`. It then writes
   `hostname` and an empty `machine-id`.
3. Sets up the API filesystems:
   - mounts a fresh `/proc` for the new PID namespace, with `/proc/sys` and
     `/proc/sysrq-trigger` remounted read-only;
   - rbind-mounts `/sys` from the host, then remounts it and every mount below
     it (including the cgroup tree) read-only;
   - creates a tmpfs `/dev` containing bind-mounted `null zero full random
     urandom tty`, a private `devpts` instance and `/dev/shm`;
   - mounts a tmpfs on `/run`.
4. Sets up `/scratch`. By default it is a tmpfs. With `SetScratch()` /
   `--scratchsize`, it is a bind mount of a host directory
   `fake-scratch.XXXX`, created in the scratch path and removed on cleanup.
   The size is not enforced.
5. Rbind-mounts every volume. This includes the static volumes (`/usr`, and
   `/bin` etc. on non-merged-usr hosts), which are then remounted read-only.
   It also includes `/run/fakemachine` and user volumes.
6. Copies extra content (for example the test binary for
   `RunInMachineWithArgs`).
7. Runs `pivot_root` into the new root and detaches the old one.
8. Runs the command with a clean environment
   (`HOME`, `PATH`, `IN_FAKE_MACHINE=yes`, `TERM` and the user's environment
   variables), with `/scratch` as the working directory. It writes the exit
   code to `/run/fakemachine/result`, the same contract the VM backends use.
9. Empties a host-backed `/scratch` from inside the namespace, since it may hold
   files owned by subordinate ids which the invoking user can't remove from
   the host.

## Isolation

The following are private to the machine:

- The filesystem view. The root is a tmpfs, the host directories are bind
  mounts, the static host directories are read-only, and nothing else of the
  host filesystem is visible.
- PIDs. The machine cannot see or signal host processes. `--kill-child` makes
  sure every process in the machine dies when fakemachine exits.
- The hostname (UTS namespace) and SysV IPC. The hostname is set to
  `fakemachine` with `hostname(1)`. If that binary is missing, fakemachine
  falls back to writing `/proc/sys/kernel/hostname`, which only host root may
  do. When not running as root the fallback fails silently and the machine
  keeps the host's hostname, although changing it still cannot affect the
  host.
- Root identity. The user is uid 0 inside the machine but is mapped to the
  invoking user on the host. Other ids are mapped to the user's first
  subordinate id range. Root in the machine has no more privileges on host
  resources than the invoking user. Files created in volumes by non-root ids
  are owned by subordinate ids on the host; the user can read them if their
  mode allows it, but may need `unshare --map-auto --map-root-user` to remove
  them.

The following are shared with the host:

- The kernel, including its loaded modules and sysctls. `/sys`, `/proc/sys`
  and `/proc/sysrq-trigger` are read-only, since when fakemachine runs as root,
  root in the machine is host root and could otherwise change them.
- The network. There is no network namespace: the machine uses the host's
  interfaces and `resolv.conf`, and `localhost` refers to the host. This
  differs from the VM backends.
- `/sys`, as a read-only recursive bind mount.
- Resources. Memory and CPU settings (`-m`, `-c`) are ignored, and a note
  is printed when they are set.

## Why images are not supported

`CreateImage()` / `--image` exposes a file as a block device, and debos then
partitions, formats and mounts that device. None of these steps is possible
without real privileges:

- **Loop devices.** `/dev/loop-control` and the `LOOP_*` ioctls require
  `CAP_SYS_ADMIN` in the *initial* user namespace. Being root in a child user
  namespace is not enough, and the kernel has no namespaced loop devices.
- **Mounting block-backed filesystems.** A filesystem type can be mounted from
  a non-initial user namespace only if it sets `FS_USERNS_MOUNT`. These types
  set it:
  - tmpfs, ramfs, proc, devpts, mqueue;
  - sysfs, but only with a new network namespace;
  - overlayfs (since 5.11), FUSE, and bind mounts.

  ext4, vfat, btrfs, xfs and the others do not set it, because their parsers are
  not hardened against malicious images.
- **Device nodes.** `mknod` of block or character devices is denied in a user
  namespace. Mounts created there implicitly get `nodev` semantics for devices
  that were not created by the host. Bind-mounting the host's `/dev/loopN`
  therefore does not help either.
So `Prepare()` refuses to start when images were added and returns a clear
error pointing users at the VM backends.

## Paths for debos disk images

All of these can run without privileges, so they could run inside the unshare
backend once debos stops assuming an attached block device.

### Build filesystem images from directories

Build the root filesystem as a plain directory (debootstrap/mmdebstrap,
apt, overlays). Then turn it into filesystem images with tools that write
image files directly:

| Filesystem | Tool |
|------------|------|
| ext2/3/4   | `mkfs.ext4 -d <dir> <img>` (e2fsprogs ≥ 1.43) |
| vfat       | `mkfs.vfat` + `mcopy -s -i <img>` (mtools) |
| btrfs      | `mkfs.btrfs --rootdir <dir> <img>` |
| erofs      | `mkfs.erofs <img> <dir>` |
| squashfs   | `mksquashfs <dir> <img>` |

Ownership must be captured correctly for this to work. Either:

- run under subuid mapping, so the files on disk have the right owners; or
- record ownership separately (`fakeroot`-style) and pass it in, for example
  `mkfs.ext4 -d` reading from a tarball, or `mkfs.erofs --tar`.

### Assemble the disk image

Combine the partitions into a disk image without loop devices:

- `systemd-repart --offline=yes --definitions=... --root=<dir> <img>`. This
  creates the GPT and populates the partitions from directories in one step.
- `sfdisk` on the image file, then `dd conv=notrunc seek=<offset>` for each
  filesystem image.
- `genimage`, which is config-driven, wraps the above and is used by Buildroot
  and PTXdist.

Bootloader steps that write raw sectors (u-boot, `grub-install --target=...`
into a file, `bootctl` on an ESP directory) work on image files or directories.

### FUSE

For actions that need to change an existing image in place, FUSE drivers can be
mounted from a user namespace:

- `fuse2fs` for ext2/3/4;
- `fusefat` for vfat;
- `squashfuse` / `erofsfuse` for read-only filesystems.

This needs `/dev/fuse`, which must then be bound into the machine. FUSE is
slower than a kernel driver and less complete; for example `fuse2fs` journal
handling is limited.

### Fall back to a VM backend

Recipes that need real block devices, such as LVM, LUKS, raw `dd` to
`/dev/vda`, or kernel-only filesystems, keep using `kvm` or `qemu`.

## Ownership

`--map-auto` maps ids from 1 upwards to the user's first `/etc/subuid` and
`/etc/subgid` range, so with the usual 65536 entry ids 1-65536 work. `chown` to
an unmapped id fails with `EINVAL`. Without
subordinate ids, only root is mapped, and `chown` to any other id (for example
`_apt`, `messagebus`, or files in a .deb owned by non-root users) fails, which
breaks `dpkg` and `debootstrap`.

## Validating isolation

The isolation test suite checks that a machine does not touch the host. It
runs probes inside the machine, writes a JSON report to a shared volume, and
asserts the report on the host against a per-backend contract
(`isolation_contract_test.go`):

    go test -v -run 'TestIsolation' ./... --backend=unshare

The contract records what is shared as well as what is private (for unshare:
network, host loopback services, cgroup namespace and abstract unix
sockets), so any change in either
direction fails a test and has to be made deliberately.

- `TestIsolation`: namespaces, host files, read-only static volumes, process
  visibility, denied privileged operations, `/dev`, environment, identity,
  network (including the host's loopback services), mount propagation, host
  runtime files in `/dev/shm` and `$XDG_RUNTIME_DIR`, escapes from the root
  (`/proc/1/root`, `..`, chroot) and further privileged kernel interfaces
  (clock, BPF, perf, `open_by_handle_at`).
- `TestIsolationExit`: a program which exits non-zero with a background child
  running passes its status on and leaves nothing behind.
- `TestIsolationCleanup`: the host is unchanged after a run, including with
  `SetScratch`.
- `TestIsolationSignals`: SIGTERM and SIGKILL of fakemachine leave no machine
  processes behind.
- `TestIsolationFailClosed`: the backend refuses to start without `unshare` or
  `pivot_root`.
- `TestIsolationCanary`: weakens the sandbox on purpose and requires the
  matching check to fail, proving the suite can detect a regression.

Target reproducibility is out of scope; it depends on the recipe and the build
tooling, so belongs in debos.

## Follow-up work

- **Network namespace.** Optionally use `--net` with `slirp4netns` or `pasta`
  for parity with the VM backends' user-mode networking. This would also allow
  a fresh sysfs mount.
- **Hardening.** The sandbox relies on namespaces alone. A seccomp filter,
  `no_new_privs` or Landlock would add defence in depth; the isolation tests
  would need contract entries for whatever they change.
- **Resource limits.** The backend sets none, so a runaway machine can use all
  of the host's memory and CPUs. Enforce `-m`/`-c` through a delegated cgroup v2 subtree,
  for example by running inside `systemd-run --user --scope -p MemoryMax=...`.
- **debos image abstraction.** Teach debos to build images through the
  approach under "Build filesystem images from directories" when the backend
  cannot attach block devices, instead of relying on
  `/dev/disk/by-fakemachine-label`.
- **debos chroot environment.** debos's unshare chroot method passes debos's
  own environment into the chroot, as the plain `chroot` method does. This
  includes `IN_FAKE_MACHINE`, `FAKEMACHINE_BACKEND`, `PATH` and `TERM`. Build a
  clean environment instead, as `systemd-nspawn` does: a fixed `PATH`,
  `HOME=/root` and `container=...`, plus the recipe's variables.
- **In-process setup.** Replace the generated shell script and the util-linux
  dependency with a re-exec of the fakemachine binary using
  `SysProcAttr.Cloneflags` and `UidMappings`.
- **Writable /etc.** Use an overlayfs over the host's `/etc` (or a fuller copy)
  so the guest can see and modify more configuration.

# The incus backend

The `incus` backend runs the command directly on the host kernel, inside an
unprivileged [Incus](https://linuxcontainers.org/incus/) (LXC) container. Like
the `unshare` backend it boots no VM, kernel or initrd, and its root filesystem
is put together from the host's `/usr`. The sandbox is set up by `incusd`
rather than by fakemachine, which brings these differences from `unshare`:

- It works where unprivileged user namespaces are blocked, and needs neither
  `/dev/kvm` nor `newuidmap`.
- The machine has its own network namespace, on the Incus bridge, as the VM
  backends have their own user-mode network.
- Memory and CPU settings (`-m`, `-c`) are enforced through cgroups.
- Starting takes a few seconds rather than milliseconds.

It has to be selected explicitly with `--backend=incus`. The `auto` backend
never picks it. Disk images are not supported, for the reasons given in
[unshare-backend.md](unshare-backend.md#why-images-are-not-supported); an
unprivileged container has the same limits as a user namespace.

## Setup

```sh
sudo apt install incus
sudo incus admin init --minimal
# The 'incus' group can't set raw.lxc and raw.idmap, which the backend needs
sudo adduser "$USER" incus-admin
# Optional: let files written to volumes be owned by you (see Ownership)
echo "root:$(id -u):1" | sudo tee -a /etc/subuid
echo "root:$(id -g):1" | sudo tee -a /etc/subgid
sudo systemctl restart incus
```

`Supported()` checks that `incus` is on `PATH`, that `incus query /1.0`
succeeds (so the user can reach the daemon) and that the server has the `lxc`
driver.

## How it works

`Machine.startup` calls `Prepare()`, as for the unshare backend. `Prepare()`
builds a skeleton root in `<tmpdir>/root`:

- the directory skeleton and merged-usr symlinks, shared with unshare;
- the same `/etc` files the initrd gets, plus `hostname`, a random
  `machine-id` and a systemd-networkd `.network` file;
- extra content, such as the test binary for `RunInMachineWithArgs`.

It also writes two scripts to `<tmpdir>`, which is mounted at
`/run/fakemachine`:

- `init.sh` runs as PID 1 of the container (`lxc.init.cmd` in `raw.lxc`).
  It chowns the pushed skeleton to root, mounts a tmpfs on `/scratch` unless
  a host scratch directory is used and on `/run/systemd` (`/run` is on the
  container root, which `systemd-nspawn` refuses to clean up), starts networking, touches
  `/run/fakemachine-ready` and then waits until the container is deleted.
- `run.sh` waits for the marker and runs the command with the same clean
  environment as the other backends, writing the exit code to
  `/run/fakemachine/result`.

`Start()` then:

1. Creates `fakemachine-<random>` with `incus init --empty`. The instance
   configuration (`limits.memory`, `limits.cpu`, `raw.lxc`, `raw.idmap`,
   `security.nesting`, disk devices) is passed as YAML (JSON) on stdin. Every
   other incus command runs with stdin on `/dev/null`, since `incus init`
   otherwise waits for YAML from a terminal.
2. Adds one `disk` device per volume. Static volumes (`/usr`, and `/bin` etc.
   on non-merged-usr hosts) and `/lib64` are read-only. A host scratch
   directory is a disk device on `/scratch`.
3. Pushes the skeleton with `incus file push -r -p`. This keeps symlinks, but
   the files keep their host owner, hence the chown in `init.sh`. Mount
   targets are left out of the chown so it never reaches host files.
4. Starts the container and runs `incus exec <name> -t|-T -- /bin/sh
   /run/fakemachine/run.sh` with stdio passed through. `-t` is used when stdin
   and stdout are terminals. `incus exec` returns the command's exit status and
   forwards `SIGINT`; fakemachine forwards `SIGTERM` and `SIGHUP`.
5. Always deletes the instance with `incus delete --force`, including when
   interrupted. The instance is not ephemeral, so it and its log
   (`incus info --show-log`) survive a failed start until then.

### Networking

There is no udev in the container, and systemd-networkd waits for udev to
initialise links while `/sys` is writable. `init.sh` therefore remounts `/sys`
read-only, then starts the host's `systemd-networkd` and waits up to 30s for
`eth0` with `systemd-networkd-wait-online`. The `.network` file uses
`ClientIdentifier=mac`, as the machine-id is random for each run. If the host has no
systemd-networkd the machine has no network and a note is printed.

A host using a local resolver such as systemd-resolved has only loopback
nameservers in `resolv.conf`, which do not work in the container's network
namespace. In that case `init.sh` points `resolv.conf` at the default gateway,
the Incus bridge, which runs dnsmasq.

## Ownership

Root in the container is mapped to a subordinate id of `incusd`, not to the
invoking user, so without more setup files written to volumes would be owned
by an unrelated id on the host. The backend handles this in one of two ways:

- If `/etc/subuid` and `/etc/subgid` give root the invoking user's uid and
  gid (`root:<uid>:1`), it sets `raw.idmap` so container root is the invoking
  user, as with the unshare backend. Other ids still map to `incusd`'s range.
- Otherwise it sets `shift=true` on volumes, an idmapped mount which makes
  container root host root on them, and prints a warning. Files written to
  volumes are then owned by root on the host.

When fakemachine runs as root the volumes are always shifted, without a
warning. Static volumes are always shifted, since they are owned by root.

Debian and Ubuntu install a `root:100000:65536` range for `incusd`; the
`root:<uid>:1` entry is in addition to it. Incus has to be restarted to pick up
changes.

## Isolation

The machine is an ordinary unprivileged Incus container: it gets its own
user, mount, PID, UTS, IPC, network and cgroup namespaces, AppArmor and
seccomp profiles from `incusd`, and is limited by `limits.memory` and
`limits.cpu`. `security.nesting` is set so tools such as `systemd-nspawn` or
`unshare` can be used inside it.

`/proc/meminfo` shows the memory limit only if `lxcfs` is installed; the
cgroup limit in `/sys/fs/cgroup/memory.max` always applies.

## Follow-up work

- **VM mode.** An `incus-vm` backend which boots fakemachine's kernel and initrd
  in an Incus VM, attaching images and scratch as disk devices, to provide
  images without `/dev/kvm` access for the user.
- **CI.** A job which installs Incus, runs `incus admin init --minimal` and
  runs the tests with `-backend=incus`.
- **Remote servers.** The backend assumes the default remote is local, since
  volumes are host paths.

// Copyright (c) Qualcomm Technologies, Inc. and/or its subsidiaries.

//go:build linux && (arm64 || amd64)

package fakemachine

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"al.essio.dev/pkg/shellescape"
	"golang.org/x/sys/unix"
)

// The unshare backend runs the command directly on the host kernel inside new
// user, mount, PID, UTS and IPC namespaces. No VM, kernel or initrd is
// involved, so it runs close to native speed and does not need root; however
// block device images cannot be supported, see doc/unshare-backend.md.
type unshareBackend struct {
	machine *Machine
}

func newUnshareBackend(m *Machine) unshareBackend {
	return unshareBackend{machine: m}
}

func (b unshareBackend) Name() string {
	return "unshare"
}

// Namespaces created for the machine
var unshareArgs = []string{
	"--user",
	"--mount", "--propagation", "private",
	"--pid", "--fork", "--kill-child",
	"--uts", "--ipc",
}

// Id mappings to try, in order of preference. Root in the machine is always the
// invoking user; with --map-auto the remaining ids are mapped to the user's
// first /etc/subuid and /etc/subgid block (via newuidmap/newgidmap), so that
// chown to other ids works as needed by tar, dpkg and debootstrap
var unshareIDMaps = [][]string{
	{"--map-auto", "--map-root-user"},
	{"--map-root-user"},
}

// When already root (e.g. in a CI container) there are normally no subordinate
// ids, but the full id range can be mapped through directly without newuidmap
var unshareRootIDMap = []string{"--map-users=all", "--map-groups=all"}

// Probe for the first working id mapping; the result is cached as the probe
// spawns processes and is needed by both Supported and Start
var unshareIDMap struct {
	once sync.Once
	args []string
	err  error
}

func unshareProbe(unshare string) ([]string, error) {
	unshareIDMap.once.Do(func() {
		var errs []string
		idmaps := unshareIDMaps
		if os.Geteuid() == 0 {
			idmaps = append([][]string{unshareRootIDMap}, idmaps...)
		}
		for _, idmap := range idmaps {
			args := append(append([]string{}, unshareArgs...), idmap...)
			out, err := exec.Command(unshare, append(args, "true")...).CombinedOutput()
			if err == nil {
				unshareIDMap.args = args
				if len(idmap) == 1 {
					fmt.Fprintf(os.Stderr, "Warning: unshare backend can't map subordinate ids "+
						"(needs newuidmap/newgidmap and /etc/subuid and /etc/subgid entries); only "+
						"root is mapped so chown to other users will fail: %s\n", strings.Join(errs, "; "))
				}
				return
			}
			errs = append(errs, fmt.Sprintf("%s: %v: %s", strings.Join(idmap, " "), err,
				strings.TrimSpace(string(out))))
		}
		unshareIDMap.err = fmt.Errorf("unable to create user namespace; unprivileged user namespaces "+
			"may be disabled (kernel.unprivileged_userns_clone, AppArmor restrict_unprivileged_userns "+
			"or a container seccomp profile): %s", strings.Join(errs, "; "))
	})
	return unshareIDMap.args, unshareIDMap.err
}

func (b unshareBackend) Supported() (bool, error) {
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		return false, fmt.Errorf("failed to find unshare binary (util-linux): %w", err)
	}

	// Shipped in util-linux-extra on Debian and Ubuntu
	if _, err := exec.LookPath("pivot_root"); err != nil {
		return false, fmt.Errorf("failed to find pivot_root binary (util-linux, or "+
			"util-linux-extra on Debian/Ubuntu): %w", err)
	}

	if _, err := unshareProbe(unshare); err != nil {
		return false, err
	}

	return true, nil
}

// The release of the kernel the host is running
func hostKernelRelease() (string, error) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", fmt.Errorf("failed to get kernel release: %w", err)
	}

	n := bytes.IndexByte(u.Release[:], 0)
	if n < 0 {
		n = len(u.Release)
	}
	return string(u.Release[:n]), nil
}

func (b unshareBackend) KernelRelease() (string, error) {
	return hostKernelRelease()
}

// The remaining VM-specific methods are not used by this backend since
// startup calls Prepare instead of building an initrd

func (b unshareBackend) KernelPath() (string, error) {
	return "", nil
}

func (b unshareBackend) ModulePath() (string, error) {
	return "", nil
}

func (b unshareBackend) UdevRules() []string {
	return []string{}
}

func (b unshareBackend) JobOutputTTY() string {
	return ""
}

func (b unshareBackend) MountParameters(_ mountPoint) (string, []string) {
	return "bind", nil
}

func (b unshareBackend) InitModules() []string {
	return []string{}
}

func (b unshareBackend) InitStaticVolumes() []mountPoint {
	return []mountPoint{}
}

func (b unshareBackend) setupScriptPath(tmpdir string) string {
	return path.Join(tmpdir, "setup.sh")
}

// Prepare writes the setup script which is run as PID 1 inside the namespaces.
// It builds a tmpfs root populated with bind mounts of the host, pivots into it
// and runs the command, recording its exit code in /run/fakemachine/result.
func (b unshareBackend) Prepare(tmpdir, command string, extracontent [][2]string) error {
	m := b.machine
	q := shellescape.Quote

	if len(m.images) > 0 {
		return fmt.Errorf("the unshare backend does not support images; use a VM backend (kvm/qemu), " +
			"see doc/unshare-backend.md")
	}

	if !m.quiet && m.memory != 2048 {
		fmt.Println("Note: the unshare backend does not limit memory or CPUs")
	}

	root := path.Join(tmpdir, "root")
	if err := os.Mkdir(root, 0755); err != nil {
		return fmt.Errorf("failed to create root directory: %w", err)
	}

	var s strings.Builder
	line := func(format string, a ...any) {
		fmt.Fprintf(&s, format+"\n", a...)
	}

	line("set -e")
	line("export PATH=/usr/sbin:/usr/bin:/sbin:/bin")
	line("ROOT=%s", q(root))

	// Root filesystem skeleton
	line(`mount -t tmpfs -o mode=0755 fakemachine-root "$ROOT"`)
	// $ROOT lives inside the /run/fakemachine volume (and possibly inside
	// user volumes); make it unbindable so recursive bind mounts of those
	// volumes don't replicate the machine root into itself
	line(`mount --make-unbindable "$ROOT"`)
	line(`cd "$ROOT"`)
	line("mkdir -p usr etc root proc sys dev run scratch tmp var/tmp var/lib/dbus .oldroot")
	line("chmod 1777 tmp var/tmp")
	line("ln -s ../run var/run")
	if m.mergedUsr {
		line("ln -s usr/bin bin")
		line("ln -s usr/sbin sbin")
		line("ln -s usr/lib lib")
		line("ln -s usr/lib64 lib64")
	} else {
		line("mkdir -p bin sbin lib")
		if _, err := os.Stat("/lib64"); err == nil {
			// /lib64 is not a static volume, but holds the dynamic linker
			line("mkdir -p lib64")
			line(`mount --rbind /lib64 "$ROOT/lib64"`)
		}
	}

	// Core system configuration, matching what buildInitrd provides
	for _, f := range []string{"/etc/passwd", "/etc/group", "/etc/nsswitch.conf",
		"/etc/ld.so.conf", "/etc/ld.so.cache", "/etc/resolv.conf"} {
		if _, err := os.Stat(f); err == nil {
			line(`cp -L %s "$ROOT"%s`, q(f), q(f))
		}
	}
	if _, err := os.Stat("/etc/ld.so.conf.d"); err == nil {
		line(`cp -rL /etc/ld.so.conf.d "$ROOT/etc/"`)
	}
	line(`echo fakemachine > "$ROOT/etc/hostname"`)
	line(`: > "$ROOT/etc/machine-id"`)

	// API filesystems
	line(`mount -t proc proc "$ROOT/proc"`)
	line(`mount --rbind /sys "$ROOT/sys"`)
	line(`mount -t tmpfs -o mode=0755,nosuid dev "$ROOT/dev"`)
	for _, d := range []string{"null", "zero", "full", "random", "urandom", "tty"} {
		line(`touch "$ROOT/dev/%[1]s" && mount --bind /dev/%[1]s "$ROOT/dev/%[1]s"`, d)
	}
	line(`mkdir "$ROOT/dev/pts" "$ROOT/dev/shm"`)
	line(`mount -t devpts -o newinstance,ptmxmode=0666,mode=0620 devpts "$ROOT/dev/pts"`)
	line(`ln -s pts/ptmx "$ROOT/dev/ptmx"`)
	line(`mount -t tmpfs -o mode=1777,nosuid,nodev shm "$ROOT/dev/shm"`)
	line(`ln -s /proc/self/fd "$ROOT/dev/fd"`)
	line(`ln -s /proc/self/fd/0 "$ROOT/dev/stdin"`)
	line(`ln -s /proc/self/fd/1 "$ROOT/dev/stdout"`)
	line(`ln -s /proc/self/fd/2 "$ROOT/dev/stderr"`)
	line(`mount -t tmpfs -o mode=0755,nosuid,nodev run "$ROOT/run"`)

	// Scratch space; an ext4 image can't be mounted unprivileged so on-disk
	// scratch is a host directory instead
	if m.scratchsize > 0 {
		scratchpath := m.scratchpath
		if scratchpath == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory for scratch path: %w", err)
			}
			scratchpath = cwd
		}
		scratchdir, err := os.MkdirTemp(scratchpath, "fake-scratch.")
		if err != nil {
			return fmt.Errorf("failed to create scratch directory: %w", err)
		}
		m.scratchfile = scratchdir
		line(`mount --bind %s "$ROOT/scratch"`, q(scratchdir))
	} else {
		line(`mount -t tmpfs -o mode=1777 scratch "$ROOT/scratch"`)
	}

	// Volumes; static volumes (/usr etc) are made read-only so a root user on
	// the host can't modify the host system from inside the machine
	for _, v := range m.mounts {
		src, err := filepath.Abs(v.hostDirectory)
		if err != nil {
			return fmt.Errorf("failed to resolve path of %s: %w", v.hostDirectory, err)
		}
		dst := `"$ROOT"` + q(v.machineDirectory)
		line("mkdir -p %s", dst)
		line("mount --rbind %s %s", q(src), dst)
		if v.static {
			line("mount -o remount,bind,ro %s || echo \"WARNING: failed to make %s read-only\" >&2",
				dst, q(v.machineDirectory))
		}
	}

	for _, v := range extracontent {
		// The script runs from $ROOT, so relative sources (e.g. os.Args[0]
		// being ../debos) must be resolved against the current directory
		src, err := filepath.Abs(v[0])
		if err != nil {
			return fmt.Errorf("failed to resolve path of %s: %w", v[0], err)
		}
		dst := `"$ROOT"` + q(path.Join("/", v[1]))
		line("mkdir -p %s", `"$(dirname `+dst+`)"`)
		line("cp %s %s", q(src), dst)
	}

	line(`hostname fakemachine 2>/dev/null || echo fakemachine > "$ROOT/proc/sys/kernel/hostname" || true`)

	line("pivot_root . .oldroot")
	line("cd /")
	line("umount -l /.oldroot")
	line("rmdir /.oldroot")

	// Run the job; the exit code is passed back through the result file as
	// for the VM backends
	env := []string{"HOME=/root", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"IN_FAKE_MACHINE=yes", "FAKEMACHINE_BACKEND=" + b.Name()}
	if term, ok := os.LookupEnv("TERM"); ok {
		env = append(env, "TERM="+term)
	}
	env = append(env, m.Environ...)
	for i := range env {
		env[i] = q(env[i])
	}

	line("set +e")
	line("cd /scratch")
	line("env -i %s /bin/sh -c %s", strings.Join(env, " "), q(command))
	line("echo $? > /run/fakemachine/result")

	// With subordinate ids mapped, a host backed scratch may contain files the
	// invoking user can't remove from the host, so empty it from in here
	if m.scratchsize > 0 {
		line("cd /")
		line("find /scratch -xdev -mindepth 1 -delete")
	}

	if err := os.WriteFile(b.setupScriptPath(tmpdir), []byte(s.String()), 0755); err != nil {
		return fmt.Errorf("failed to write setup script: %w", err)
	}

	return nil
}

func (b unshareBackend) Start() (bool, error) {
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		return false, fmt.Errorf("failed to find unshare binary: %w", err)
	}

	tmpdir := ""
	for _, v := range b.machine.mounts {
		if v.machineDirectory == "/run/fakemachine" {
			tmpdir = v.hostDirectory
		}
	}
	if tmpdir == "" {
		return false, fmt.Errorf("fakemachine run directory not set up")
	}

	nsargs, err := unshareProbe(unshare)
	if err != nil {
		return false, err
	}

	args := append([]string{unshare}, nsargs...)
	args = append(args, "/bin/sh", b.setupScriptPath(tmpdir))

	pa := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	p, err := os.StartProcess(unshare, args, &pa)
	if err != nil {
		return false, fmt.Errorf("failed to start unshare process: %w", err)
	}

	pstate, err := p.Wait()
	if err != nil {
		return false, fmt.Errorf("error waiting for unshare process: %w", err)
	}

	if !pstate.Success() {
		return false, fmt.Errorf("machine setup failed (%s); see the output above", pstate)
	}

	return true, nil
}

// Copyright (c) Qualcomm Technologies, Inc. and/or its subsidiaries.

//go:build linux && (arm64 || amd64)

package fakemachine

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"al.essio.dev/pkg/shellescape"
	"golang.org/x/sys/unix"
)

// The incus backend runs the command on the host kernel inside an unprivileged
// Incus (LXC) container. Unlike the unshare backend the sandbox is set up by
// incusd, so it works where unprivileged user namespaces are blocked, has its
// own network namespace and limits memory and CPUs; block device images still
// can't be supported, see doc/incus-backend.md.
type incusContainerBackend struct {
	machine *Machine
}

func newIncusContainerBackend(m *Machine) incusContainerBackend {
	return incusContainerBackend{machine: m}
}

func (b incusContainerBackend) Name() string {
	return "incus"
}

func (b incusContainerBackend) Supported() (bool, error) {
	incus, err := exec.LookPath("incus")
	if err != nil {
		return false, fmt.Errorf("failed to find incus binary: %w", err)
	}

	// raw.lxc and raw.idmap need full access, which the incus group doesn't give
	//nolint:noctx,gosec // Synchronous operation; incus is resolved from PATH and no shell is invoked.
	out, err := exec.Command(incus, "query", "/1.0").Output()
	if err != nil {
		var stderr string
		ee := &exec.ExitError{}
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}

		return false, fmt.Errorf("unable to query incus (is the user in the incus-admin group?): "+
			"%w: %s", err, stderr)
	}

	var info struct {
		Environment struct {
			Driver string `json:"driver"`
		} `json:"environment"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return false, fmt.Errorf("failed to parse incus server info: %w", err)
	}
	if !strings.Contains(info.Environment.Driver, "lxc") {
		return false, fmt.Errorf("incus server has no container (lxc) driver: %q",
			info.Environment.Driver)
	}

	return true, nil
}

func (b incusContainerBackend) KernelRelease() (string, error) {
	return hostKernelRelease()
}

// The remaining VM-specific methods are not used by this backend since
// startup calls Prepare instead of building an initrd

func (b incusContainerBackend) KernelPath() (string, error) {
	return "", nil
}

func (b incusContainerBackend) ModulePath() (string, error) {
	return "", nil
}

func (b incusContainerBackend) UdevRules() []string {
	return []string{}
}

func (b incusContainerBackend) JobOutputTTY() string {
	return ""
}

func (b incusContainerBackend) MountParameters(_ mountPoint) (string, []string) {
	return "bind", nil
}

func (b incusContainerBackend) InitModules() []string {
	return []string{}
}

func (b incusContainerBackend) InitStaticVolumes() []mountPoint {
	return []mountPoint{}
}

// networkd uses a client identifier based on the MAC address rather than on the
// machine-id, which is random for each run.
const incusNetworkTemplate = `[Match]
Type=ether

[Network]
DHCP=ipv4
LinkLocalAddressing=no
IPv6AcceptRA=no

[DHCPv4]
ClientIdentifier=mac
`

// A root filesystem skeleton built on the host and pushed into the container;
// the machine paths created are recorded so they can be chowned to root from
// inside the container, as incus file push keeps the host ownership.
type incusRoot struct {
	dir   string
	paths []string
}

func (r *incusRoot) mkdir(p string) error {
	cur := "/"
	for c := range strings.SplitSeq(strings.Trim(path.Clean(p), "/"), "/") {
		cur = path.Join(cur, c)
		//nolint:gosec // The root filesystem directories must be accessible to the container.
		err := os.Mkdir(path.Join(r.dir, cur), 0o755)
		if err == nil {
			r.paths = append(r.paths, cur)
		} else if !os.IsExist(err) {
			return fmt.Errorf("failed to create %s: %w", cur, err)
		}
	}

	return nil
}

func (r *incusRoot) symlink(target, p string) error {
	p = path.Join("/", p)
	if err := os.Symlink(target, path.Join(r.dir, p)); err != nil {
		return fmt.Errorf("failed to create symlink %s: %w", p, err)
	}
	r.paths = append(r.paths, p)

	return nil
}

func (r *incusRoot) writeFile(p string, data string) error {
	p = path.Join("/", p)
	if err := r.mkdir(path.Dir(p)); err != nil {
		return err
	}
	//nolint:gosec // Files in the root filesystem must be readable by the container.
	if err := os.WriteFile(path.Join(r.dir, p), []byte(data), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", p, err)
	}
	r.paths = append(r.paths, p)

	return nil
}

// Copy a host file or directory (following symlinks) into the root.
func (r *incusRoot) copy(src, p string) error {
	p = path.Join("/", p)
	if err := r.mkdir(path.Dir(p)); err != nil {
		return err
	}
	dst := path.Join(r.dir, p)
	//nolint:noctx,gosec // Synchronous operation; the executable is fixed and no shell is invoked.
	if out, err := exec.Command("cp", "-rL", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to copy %s: %w: %s", src, err, strings.TrimSpace(string(out)))
	}

	//nolint:wrapcheck // WalkDir errors already include the path.
	return filepath.WalkDir(dst, func(f string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r.paths = append(r.paths, path.Join(p, strings.TrimPrefix(f, dst)))

		return nil
	})
}

// The machine directories which are mounted from the host as disk devices.
func (b incusContainerBackend) mountTargets() []string {
	m := b.machine
	var targets []string
	for _, v := range m.mounts {
		targets = append(targets, path.Clean(v.machineDirectory))
	}
	if m.scratchfile != "" {
		targets = append(targets, "/scratch")
	}
	if hostLib64(m) {
		targets = append(targets, "/lib64")
	}

	return targets
}

func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}

	return false
}

// The first of the given paths which exists on the host.
func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}

// Prepare builds the root filesystem skeleton and writes the scripts run in the
// container: init.sh runs as PID 1 and sets up the machine, run.sh is run with
// incus exec once that is done and runs the command, recording its exit code
// in /run/fakemachine/result.
func (b incusContainerBackend) Prepare(tmpdir, command string, extracontent [][2]string) error {
	m := b.machine
	q := shellescape.Quote

	if err := rejectImages(m, b.Name()); err != nil {
		return err
	}

	if m.scratchsize > 0 {
		if _, err := hostScratchDir(m); err != nil {
			return err
		}
	}

	r := incusRoot{dir: path.Join(tmpdir, "root")}
	//nolint:gosec // The root filesystem must be accessible to the container.
	if err := os.Mkdir(r.dir, 0o755); err != nil {
		return fmt.Errorf("failed to create root directory: %w", err)
	}

	dirs, symlinks := rootSkeleton(m)
	for _, d := range dirs {
		if err := r.mkdir(d); err != nil {
			return err
		}
	}
	for _, d := range []string{"tmp", "var/tmp"} {
		if err := os.Chmod(path.Join(r.dir, d), 0o777|os.ModeSticky); err != nil {
			return fmt.Errorf("failed to set permissions of /%s: %w", d, err)
		}
	}
	for _, l := range symlinks {
		if err := r.symlink(l[0], l[1]); err != nil {
			return err
		}
	}

	for _, f := range hostEtcFiles() {
		if err := r.copy(f, f); err != nil {
			return err
		}
	}
	if err := r.writeFile("/etc/hostname", "fakemachine\n"); err != nil {
		return err
	}
	// There is no init to set up a machine-id and tools such as
	// systemd-nspawn fail without one
	machineID := make([]byte, 16)
	if _, err := rand.Read(machineID); err != nil {
		return fmt.Errorf("failed to generate machine-id: %w", err)
	}
	if err := r.writeFile("/etc/machine-id", hex.EncodeToString(machineID)+"\n"); err != nil {
		return err
	}

	networkd := firstExisting("/usr/lib/systemd/systemd-networkd", "/lib/systemd/systemd-networkd")
	waitOnline := firstExisting("/usr/lib/systemd/systemd-networkd-wait-online",
		"/lib/systemd/systemd-networkd-wait-online")
	if networkd != "" && waitOnline != "" {
		if err := r.writeFile("/etc/systemd/network/80-fakemachine.network", incusNetworkTemplate); err != nil {
			return err
		}
	} else if !m.quiet {
		fmt.Println("Note: systemd-networkd not found on the host; the incus machine has no network")
	}

	for _, v := range extracontent {
		src, err := filepath.Abs(v[0])
		if err != nil {
			return fmt.Errorf("failed to resolve path of %s: %w", v[0], err)
		}
		if err := r.copy(src, v[1]); err != nil {
			return err
		}
	}

	var s strings.Builder
	line := func(format string, a ...any) {
		fmt.Fprintf(&s, format+"\n", a...)
	}

	line("export PATH=/usr/sbin:/usr/bin:/sbin:/bin")

	// Chown what was pushed, skipping anything that is mounted over (which
	// would change the host files) and the API filesystems set up by LXC
	targets := append(b.mountTargets(), "/proc", "/sys", "/dev")
	var owned []string
	for _, p := range r.paths {
		if !underAny(p, targets) {
			owned = append(owned, q(p))
		}
	}
	line(`chown -h 0:0 %s || echo "WARNING: failed to chown the machine root" >&2`,
		strings.Join(owned, " "))

	if m.scratchfile == "" {
		line("mount -t tmpfs -o mode=1777 scratch /scratch")
	}
	// /run is on the container root rather than a tmpfs and systemd tools
	// (e.g. systemd-nspawn) refuse to clean up their state under it; a tmpfs
	// on all of /run would hide the /run/fakemachine disk device
	line("mkdir -p /run/systemd")
	line("mount -t tmpfs -o mode=755,nosuid,nodev systemd /run/systemd")
	line("mkdir -p /dev/shm")
	line("grep -q ' /dev/shm ' /proc/mounts || mount -t tmpfs -o mode=1777,nosuid,nodev shm /dev/shm")

	if networkd != "" && waitOnline != "" {
		// With /sys writable networkd waits for udev to initialise links,
		// which never happens as there is no udev in the container
		line(`mount -o remount,bind,ro /sys || echo "WARNING: failed to make /sys read-only" >&2`)
		line("mkdir -p /run/systemd/netif")
		line("chown systemd-network:systemd-network /run/systemd/netif 2>/dev/null")
		line("%s &", q(networkd))
		line(`%s -q -i eth0 -4 --timeout=30 || echo "WARNING: Network setup failed" >&2`, q(waitOnline))
		// A host using a local resolver (e.g. systemd-resolved) has only
		// loopback nameservers, which don't work in the container's own
		// network namespace; use the DNS server on the incus bridge instead
		line(`if ! grep -E '^nameserver[[:space:]]' /etc/resolv.conf 2>/dev/null | ` +
			`grep -qvE '[[:space:]](127\.|::1)'; then`)
		line(`  gw=$(ip -4 route show default 2>/dev/null | awk '{print $3; exit}')`)
		line(`  [ -n "$gw" ] && echo "nameserver $gw" > /etc/resolv.conf`)
		line("fi")
	}

	line("touch /run/fakemachine-ready")
	// Stay around as PID 1, reaping orphaned processes, until the container
	// is deleted
	line("trap 'exit 0' TERM PWR")
	line("while :; do sleep 3600 & wait $!; done")

	//nolint:gosec // The init script is intentionally executable.
	if err := os.WriteFile(path.Join(tmpdir, "init.sh"), []byte(s.String()), 0o755); err != nil {
		return fmt.Errorf("failed to write init script: %w", err)
	}

	s.Reset()
	line("while [ ! -e /run/fakemachine-ready ]; do sleep 0.1; done")
	s.WriteString(jobScript(m, b.Name(), command))

	//nolint:gosec // The job script is intentionally executable.
	if err := os.WriteFile(path.Join(tmpdir, "run.sh"), []byte(s.String()), 0o755); err != nil {
		return fmt.Errorf("failed to write run script: %w", err)
	}

	return nil
}

// Whether the subordinate id file allows root (i.e. incusd) to map id.
func subidAllowsRoot(file string, id int) bool {
	//nolint:gosec // file is a fixed subordinate id file path.
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Split(strings.TrimSpace(scanner.Text()), ":")
		if len(fields) != 3 || (fields[0] != "root" && fields[0] != "0") {
			continue
		}
		start, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 == nil && err2 == nil && id >= start && id < start+count {
			return true
		}
	}

	return false
}

// The raw.idmap mapping the invoking user to root in the container, so files
// written to volumes are owned by that user as with the unshare backend. If
// that isn't allowed, volumes are idmapped (shift) instead, so that container
// root is host root on them.
func incusIDMap() string {
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		return ""
	}
	if !subidAllowsRoot("/etc/subuid", uid) || !subidAllowsRoot("/etc/subgid", gid) {
		fmt.Fprintf(os.Stderr, "Warning: incus can't map uid %d to root in the machine (needs "+
			"root:%d:1 in /etc/subuid and root:%d:1 in /etc/subgid, then restart incus); files "+
			"written to volumes will be owned by root\n", uid, uid, gid)

		return ""
	}

	return fmt.Sprintf("uid %d 0\ngid %d 0", uid, gid)
}

// Run incus without a terminal on stdin, as otherwise e.g. incus init waits
// for YAML on stdin.
func incusCommand(args ...string) error {
	//nolint:noctx,gosec // Synchronous operation; the executable is fixed and no shell is invoked.
	out, err := exec.Command("incus", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("incus %s failed: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}

	return nil
}

// Start a detached process which deletes the instance once fakemachine has
// died without cleaning up. It holds the read end of a pipe: the returned
// function writes a line, telling it the instance is already gone, while a
// fakemachine that is killed only closes the pipe.
func startIncusReaper(name string) (func(), error) {
	incus, err := exec.LookPath("incus")
	if err != nil {
		return nil, fmt.Errorf("failed to find incus binary: %w", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create reaper pipe: %w", err)
	}
	defer func() { _ = r.Close() }()

	//nolint:noctx,gosec // The reaper outlives any context; the script is fixed and the instance name is passed as an argument.
	cmd := exec.Command("/bin/sh", "-c", `read _ || exec "$0" delete --force "$1"`, incus, name)
	cmd.Stdin = r
	// Its own session, so neither a terminal interrupt nor the death of
	// fakemachine's process group takes it down
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = w.Close()

		return nil, fmt.Errorf("failed to start instance reaper: %w", err)
	}
	go func() { _ = cmd.Wait() }()

	return func() {
		_, _ = w.WriteString("done\n")
		_ = w.Close()
	}, nil
}

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)

	return err == nil
}

func (b incusContainerBackend) Start() (bool, error) {
	m := b.machine

	tmpdir := ""
	for _, v := range m.mounts {
		if v.machineDirectory == "/run/fakemachine" {
			tmpdir = v.hostDirectory
		}
	}
	if tmpdir == "" {
		return false, fmt.Errorf("fakemachine run directory not set up")
	}

	id := make([]byte, 4)
	if _, err := rand.Read(id); err != nil {
		return false, fmt.Errorf("failed to generate instance name: %w", err)
	}
	name := "fakemachine-" + hex.EncodeToString(id)

	idmap := incusIDMap()
	shift := idmap == ""

	rawLxc := "lxc.init.cmd = /bin/sh /run/fakemachine/init.sh\nlxc.uts.name = fakemachine\n"
	config := map[string]string{
		"limits.memory":    fmt.Sprintf("%dMiB", m.memory),
		"limits.cpu":       strconv.Itoa(m.numcpus),
		"raw.lxc":          rawLxc,
		"security.nesting": "true",
	}
	if idmap != "" {
		config["raw.idmap"] = idmap
	}

	devices := map[string]map[string]string{}
	disk := func(src, dst string, static bool) error {
		src, err := filepath.Abs(src)
		if err != nil {
			return fmt.Errorf("failed to resolve path of %s: %w", src, err)
		}
		d := map[string]string{"type": "disk", "source": src, "path": dst}
		// Static volumes are owned by host root, so are always shifted
		if shift || static {
			d["shift"] = "true"
		}
		// Read-only so a root user on the host can't modify the host
		// system from inside the machine
		if static {
			d["readonly"] = "true"
		}
		devices[fmt.Sprintf("fakemachine%d", len(devices))] = d

		return nil
	}
	for _, v := range m.mounts {
		if err := disk(v.hostDirectory, v.machineDirectory, v.static); err != nil {
			return false, err
		}
	}
	if m.scratchfile != "" {
		if err := disk(m.scratchfile, "/scratch", false); err != nil {
			return false, err
		}
	}
	if hostLib64(m) {
		if err := disk("/lib64", "/lib64", true); err != nil {
			return false, err
		}
	}

	// The instance configuration is passed to incus init on stdin
	instance, err := json.Marshal(map[string]any{"config": config, "devices": devices})
	if err != nil {
		return false, fmt.Errorf("failed to encode instance configuration: %w", err)
	}

	// The instance's processes are children of incusd, so they outlive
	// fakemachine if it is killed before the deferred delete runs
	reaper, err := startIncusReaper(name)
	if err != nil {
		return false, err
	}
	defer reaper()

	// Not ephemeral, so the instance (and its log) is kept if it fails to
	// start; it is always deleted here instead
	//nolint:noctx,gosec // Synchronous operation; the executable is fixed and no shell is invoked.
	cmd := exec.Command("incus", "init", "--empty", name)
	cmd.Stdin = strings.NewReader(string(instance))
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("incus init failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	defer func() {
		if err := incusCommand("delete", "--force", name); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to delete instance %s: %v\n", name, err)
		}
	}()

	// Keep the instance deleted when interrupted; incus exec forwards the
	// signal to the job
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)

	// Push each entry of the root rather than root/., which incus 6.0 pushes
	// to /root instead of /. No -p as / exists and incus 6.0 opens the first
	// source for it, failing on symlinks such as /bin which dangle on the host
	entries, err := os.ReadDir(path.Join(tmpdir, "root"))
	if err != nil {
		return false, fmt.Errorf("failed to read root directory: %w", err)
	}
	push := []string{"file", "push", "-q", "-r"}
	for _, e := range entries {
		push = append(push, path.Join(tmpdir, "root", e.Name()))
	}
	if err := incusCommand(append(push, name+"/")...); err != nil {
		return false, err
	}

	if err := incusCommand("start", name); err != nil {
		// The instance is deleted on return, so include its log now
		//nolint:noctx,gosec // Synchronous operation; the executable is fixed and no shell is invoked.
		log, _ := exec.Command("incus", "info", "--show-log", name).CombinedOutput()

		return false, fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(log)))
	}

	tty := "-T"
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		tty = "-t"
	}

	incus, err := exec.LookPath("incus")
	if err != nil {
		return false, fmt.Errorf("failed to find incus binary: %w", err)
	}
	args := []string{
		incus, "exec", name, tty, "--cwd", "/", "--",
		"/bin/sh", "/run/fakemachine/run.sh",
	}

	pa := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	p, err := os.StartProcess(incus, args, &pa)
	if err != nil {
		return false, fmt.Errorf("failed to start incus exec: %w", err)
	}

	// An interrupt from the terminal already reached incus exec; pass on
	// anything else
	go func() {
		for sig := range signals {
			if sig != os.Interrupt {
				_ = p.Signal(sig)
			}
		}
	}()

	pstate, err := p.Wait()
	if err != nil {
		return false, fmt.Errorf("error waiting for incus exec: %w", err)
	}

	if !pstate.Success() {
		return false, fmt.Errorf("machine setup failed (%s); see the output above", pstate)
	}

	return true, nil
}

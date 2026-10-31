package fakemachine

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

var nsNames = []string{"user", "mnt", "pid", "uts", "ipc", "net", "cgroup"}

// Everything the host provides to, or leaves around for, the machine.
type hostFixture struct {
	marker      string
	secretPath  string
	isolation   string // host side of the /isolation volume
	sleeper     *exec.Cmd
	sleeperArgv string // exact /proc cmdline of the sleeper, NUL separated
	socketName  string
	leakEnv     string
	keepEnv     string
	hostAbsent  string // the probe writes here from the machine
	hostNS      map[string]string
	hostname    string
	leakSeen    bool // set by the handshake goroutine
	leakCheckCh chan struct{}
	symlinks    []string // machine paths of symlinks to the secret
	keyIDs      []int    // host keys holding the marker
	keyDesc     string
	shmKey      int // host SysV segment holding the marker
	perfAllowed bool
	loopback    string   // host TCP service on the loopback interface
	runtime     []string // host runtime files which must stay out of reach
}

func randHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	_, err := rand.Read(b)
	require.NoError(t, err)

	return hex.EncodeToString(b)
}

// A decimal token, as sleep rejects anything else.
func randDigits(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	_, err := rand.Read(b)
	require.NoError(t, err)
	var sb strings.Builder
	for _, v := range b {
		sb.WriteByte('0' + v%10)
	}

	return sb.String()
}

// The process is alive (not a zombie) and still has the given cmdline.
func sleeperAlive(pid int, cmdline string) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// The state follows the last ')' (the command name may contain anything)
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) || stat[i+2] == 'Z' {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))

	return err == nil && string(b) == cmdline
}

func newHostFixture(t *testing.T) *hostFixture {
	t.Helper()
	marker := randHex(t)
	fx := &hostFixture{
		marker:      marker,
		isolation:   t.TempDir(),
		socketName:  "fm-" + marker,
		leakEnv:     "FAKEMACHINE_LEAK_" + strings.ToUpper(marker),
		keepEnv:     "FAKEMACHINE_KEEP",
		hostNS:      map[string]string{},
		leakCheckCh: make(chan struct{}),
	}

	// A file outside every volume
	outside := t.TempDir()
	fx.secretPath = filepath.Join(outside, "secret-"+marker)
	require.NoError(t, os.WriteFile(fx.secretPath, []byte(marker), 0o600))

	// The probe writes here from inside; it must never show up on the host
	fx.hostAbsent = "/fm-probe-" + marker

	// Symlinks in a volume resolve in the machine, so neither may reach the
	// secret: one absolute, one climbing out of the volume
	rel, err := filepath.Rel(fx.isolation, fx.secretPath)
	require.NoError(t, err)
	for name, target := range map[string]string{"escape-abs": fx.secretPath, "escape-rel": rel} {
		require.NoError(t, os.Symlink(target, filepath.Join(fx.isolation, name)))
		fx.symlinks = append(fx.symlinks, filepath.Join(isolationDir, name))
	}
	sort.Strings(fx.symlinks)

	// A key in the host's keyrings, which the machine must not read
	fx.keyDesc = "fm-" + marker
	fx.keyIDs = addHostKeys(t, fx.keyDesc, marker)

	// A world-readable shared memory segment, so only the IPC namespace can
	// keep it from the machine
	fx.shmKey = addHostShm(t, marker)
	if b, err := os.ReadFile("/proc/sys/kernel/perf_event_paranoid"); err == nil {
		level, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		fx.perfAllowed = level <= 0
	}

	// What other programs leave around in the host's runtime directories: a
	// POSIX shared memory object, and a session socket where there is one
	shmFile := "/dev/shm/fm-" + marker
	//nolint:gosec // World-writable so the machine could tamper with it if not isolated.
	require.NoError(t, os.WriteFile(shmFile, []byte(marker), 0o666))
	t.Cleanup(func() { _ = os.Remove(shmFile) })
	fx.runtime = append(fx.runtime, shmFile)
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		sock := filepath.Join(dir, "fm-"+marker+".sock")
		//nolint:noctx // The listener lives for the whole test.
		if l, err := net.Listen("unix", sock); err == nil {
			t.Cleanup(func() { _ = l.Close() })
			fx.runtime = append(fx.runtime, sock)
		}
	}

	// Host process the machine must neither see nor be able to signal
	digits := randDigits(t)
	fx.sleeperArgv = "sleep\x003600." + digits + "\x00"
	//nolint:noctx,gosec // The sleeper must outlive the machine; the command is fixed.
	fx.sleeper = exec.Command("sleep", "3600."+digits)
	require.NoError(t, fx.sleeper.Start())
	require.Eventually(t, func() bool {
		return sleeperAlive(fx.sleeper.Process.Pid, fx.sleeperArgv)
	}, 5*time.Second, 10*time.Millisecond, "host sleeper did not start")
	t.Cleanup(func() {
		_ = fx.sleeper.Process.Kill()
		_ = fx.sleeper.Wait()
	})

	// An fd without CLOEXEC which a careless backend would inherit
	fd, err := syscall.Open(fx.secretPath, syscall.O_RDONLY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Close(fd) })

	t.Setenv(fx.leakEnv, marker)

	//nolint:noctx // The listener lives for the whole test.
	l, err := net.Listen("unix", "@"+fx.socketName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	// A service which only listens on the host's loopback interface
	//nolint:noctx // The listener lives for the whole test.
	tl, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = tl.Close() })
	fx.loopback = tl.Addr().String()

	for _, n := range nsNames {
		fx.hostNS[n], _ = os.Readlink("/proc/self/ns/" + n)
	}
	fx.hostname, _ = os.Hostname()

	return fx
}

func (fx *hostFixture) input() probeInput {
	return probeInput{
		Marker:         fx.marker,
		SecretPath:     fx.secretPath,
		SleeperPID:     fx.sleeper.Process.Pid,
		SleeperCmdline: fx.sleeperArgv,
		AbstractSocket: fx.socketName,
		LeakEnv:        fx.leakEnv,
		KeepEnv:        fx.keepEnv,
		HostAbsent:     []string{fx.hostAbsent},
		Symlinks:       fx.symlinks,
		KeyIDs:         fx.keyIDs,
		KeyDesc:        fx.keyDesc,
		ShmKey:         fx.shmKey,
		HostRuntime:    fx.runtime,
		PerfAllowed:    fx.perfAllowed,
		LoopbackAddr:   fx.loopback,
	}
}

// Add a user key holding data to the user keyring and, when there is one, the
// session keyring. A session keyring is never created: keyring credentials are
// per thread, so one made here would only belong to some Go thread.
func addHostKeys(t *testing.T, desc, data string) []int {
	t.Helper()
	rings := []int{unix.KEY_SPEC_USER_KEYRING}
	if _, err := unix.KeyctlGetKeyringID(unix.KEY_SPEC_SESSION_KEYRING, false); err == nil {
		rings = append(rings, unix.KEY_SPEC_SESSION_KEYRING)
	}
	var ids []int
	for _, ring := range rings {
		id, err := unix.AddKey("user", desc, []byte(data), ring)
		if err != nil {
			t.Logf("adding a host key: %v", err)

			continue
		}
		ids = append(ids, id)
		t.Cleanup(func() { _, _ = unix.KeyctlInt(unix.KEYCTL_INVALIDATE, id, 0, 0, 0) })
	}

	return ids
}

// Create a SysV shared memory segment holding data, keyed from it.
func addHostShm(t *testing.T, data string) int {
	t.Helper()
	k, err := strconv.ParseUint(data[:7], 16, 32)
	require.NoError(t, err)
	key := int(k) + 1 // never IPC_PRIVATE
	id, err := unix.SysvShmGet(key, 4096, unix.IPC_CREAT|unix.IPC_EXCL|0o666)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = unix.SysvShmCtl(id, unix.IPC_RMID, nil) })
	b, err := unix.SysvShmAttach(id, 0, 0)
	require.NoError(t, err)
	copy(b, data)
	require.NoError(t, unix.SysvShmDetach(b))

	return key
}

// Wait for the probe to mount something, check whether the host can see it,
// then let the probe continue.
func (fx *hostFixture) handshake(done <-chan struct{}) {
	defer close(fx.leakCheckCh)
	ready := filepath.Join(fx.isolation, "ready")
	for {
		select {
		case <-done:
			return
		case <-time.After(20 * time.Millisecond):
		}
		if _, err := os.Stat(ready); err != nil {
			continue
		}
		mi, _ := os.ReadFile("/proc/self/mountinfo")
		fx.leakSeen = strings.Contains(string(mi), "fm-leak-"+fx.marker)
		//nolint:gosec // Read by the machine and holds no sensitive data.
		_ = os.WriteFile(filepath.Join(fx.isolation, "ack"), nil, 0o644)

		return
	}
}

// Things on the host which a machine run must leave unchanged.
type hostSnapshot struct {
	Mounts   []string
	Hostname string
	Temp     []string
	Loops    []string
	Binfmt   []string
	Procs    []string
	Incus    []string
	Cgroups  []string // fm-probe-* directories anywhere in the cgroup tree
}

func globNames(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	sort.Strings(m)

	return m
}

func takeSnapshot(t *testing.T, marker string) hostSnapshot {
	t.Helper()
	s := hostSnapshot{}
	s.Hostname, _ = os.Hostname()

	mi, _ := os.ReadFile("/proc/self/mountinfo")
	for l := range strings.SplitSeq(string(mi), "\n") {
		// Drop the mount id and parent id, they change constantly
		if f := strings.SplitN(l, " ", 3); len(f) == 3 {
			s.Mounts = append(s.Mounts, f[2])
		}
	}
	sort.Strings(s.Mounts)

	s.Temp = globNames(filepath.Join(os.TempDir(), "fakemachine-*"))
	s.Loops = globNames("/sys/block/loop*/loop/backing_file")
	s.Binfmt = globNames("/proc/sys/fs/binfmt_misc/*")
	s.Procs = procsWithMarker(marker)
	s.Incus = incusInstances(t)
	_ = filepath.WalkDir("/sys/fs/cgroup", func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && strings.HasPrefix(d.Name(), "fm-probe-") {
			s.Cgroups = append(s.Cgroups, p)
		}

		return nil
	})

	return s
}

// Names of the host's fakemachine incus instances; nil when incus is not
// installed, since then no backend can create any.
func incusInstances(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("incus"); err != nil {
		return nil
	}
	//nolint:noctx // Synchronous query; incus is resolved from PATH.
	out, err := exec.Command("incus", "list", "--format", "csv", "--columns", "n").Output()
	if err != nil {
		t.Logf("listing incus instances: %v", err)

		return nil
	}
	var names []string
	for n := range strings.FieldsSeq(string(out)) {
		if strings.HasPrefix(n, "fakemachine-") {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	return names
}

// Processes whose cmdline is exactly argv.
func procsWithArgv(argv string) []string {
	var pids []string
	cmdlines, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, c := range cmdlines {
		//nolint:gosec // Only /proc cmdline files are read.
		if b, err := os.ReadFile(c); err == nil && string(b) == argv {
			pids = append(pids, strings.Split(c, "/")[2])
		}
	}

	return pids
}

func procsWithMarker(marker string) []string {
	var pids []string
	cmdlines, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	self := strconv.Itoa(os.Getpid())
	for _, c := range cmdlines {
		pid := strings.Split(c, "/")[2]
		if pid == self {
			continue
		}
		//nolint:gosec // Only /proc cmdline files are read.
		if b, err := os.ReadFile(c); err == nil && strings.Contains(string(b), marker) {
			pids = append(pids, pid)
		}
	}

	return pids
}

// Differences between snapshots, ignoring the sleeper which is expected.
func diffSnapshot(before, after hostSnapshot) []string {
	var d []string
	if before.Hostname != after.Hostname {
		d = append(d, fmt.Sprintf("hostname %q -> %q", before.Hostname, after.Hostname))
	}
	cmp := func(name string, a, b []string) {
		for _, x := range b {
			if !slices.Contains(a, x) {
				d = append(d, name+" appeared: "+x)
			}
		}
		for _, x := range a {
			if !slices.Contains(b, x) {
				d = append(d, name+" disappeared: "+x)
			}
		}
	}
	cmp("mount", before.Mounts, after.Mounts)
	cmp("tempdir", before.Temp, after.Temp)
	cmp("loop device", before.Loops, after.Loops)
	cmp("binfmt", before.Binfmt, after.Binfmt)
	cmp("process", before.Procs, after.Procs)
	cmp("incus instance", before.Incus, after.Incus)
	cmp("cgroup", before.Cgroups, after.Cgroups)

	return d
}

// Boot a machine, run the in-machine probes and return their report. Host
// side effects are checked here since they hold for every backend and for the
// weakened canary machines too.
// Killing the machine's pid namespace is asynchronous, so give its processes
// a moment to go before calling them leftovers. The sleeper is in both
// snapshots, anything else is a leftover.
func requireHostRestored(t *testing.T, before hostSnapshot, marker string) {
	t.Helper()
	var diff []string
	require.Eventually(t, func() bool {
		diff = diffSnapshot(before, takeSnapshot(t, marker))

		return len(diff) == 0
	}, 2*time.Second, 50*time.Millisecond, "host changed by the machine run")
	require.Empty(t, diff)
}

func runIsolationProbe(t *testing.T, m *Machine, fx *hostFixture) probeReport {
	t.Helper()

	m.AddVolumeAt(fx.isolation, isolationDir)
	m.SetEnviron([]string{fx.keepEnv + "=1"})

	//nolint:musttag // Both ends share the struct, so the field names are the keys.
	input, err := json.Marshal(fx.input())
	require.NoError(t, err)

	before := takeSnapshot(t, fx.marker)

	done := make(chan struct{})
	go fx.handshake(done)
	code, err := m.RunInMachineWithArgs([]string{
		"-test.run", "^TestIsolationProbe$", "-testarg", string(input),
	})
	close(done)
	<-fx.leakCheckCh
	require.NoError(t, err)
	require.Equal(t, 0, code, "probe failed in the machine")

	data, err := os.ReadFile(filepath.Join(fx.isolation, "report.json"))
	require.NoError(t, err)
	var r probeReport
	//nolint:musttag // Both ends share the struct, so the field names are the keys.
	require.NoError(t, json.Unmarshal(data, &r))

	requireHostRestored(t, before, fx.marker)

	_, err = os.Stat(fx.hostAbsent)
	require.True(t, os.IsNotExist(err), "%s written in the machine appeared on the host", fx.hostAbsent)

	require.True(t, sleeperAlive(fx.sleeper.Process.Pid, fx.sleeperArgv), "host sleeper was killed")

	return r
}

// Contract checks: nil when the report is within the contract

func checkHostKernelNamespaces(c isolationContract, r probeReport, host map[string]string) error {
	if !c.HostKernel {
		return nil
	}
	for _, n := range c.PrivateNS {
		if r.NS[n] == "" || r.NS[n] == host[n] {
			return fmt.Errorf("%s namespace is shared with the host (%s)", n, r.NS[n])
		}
	}
	for _, n := range c.SharedNS {
		if r.NS[n] != host[n] {
			return fmt.Errorf("%s namespace is documented as shared but differs", n)
		}
	}

	return nil
}

func checkNoHostFiles(_ isolationContract, r probeReport) error {
	if r.SecretVisible {
		return fmt.Errorf("host file outside every volume is visible")
	}
	if !r.IsolationWritten {
		return fmt.Errorf("/isolation volume is not writable")
	}

	return nil
}

func checkReadonlyStatic(c isolationContract, r probeReport) error {
	if c.ReadonlyStatic && !r.StaticReadonly {
		return fmt.Errorf("/usr is writable")
	}

	return nil
}

func checkFds(_ isolationContract, r probeReport, secretPath string) error {
	for _, f := range r.Fds {
		if strings.Contains(f, secretPath) {
			return fmt.Errorf("host descriptor inherited: %s", f)
		}
	}

	return nil
}

func checkProcesses(_ isolationContract, r probeReport) error {
	if r.SleeperInProc {
		return fmt.Errorf("host process is visible in /proc")
	}

	return nil
}

func checkHostname(_ isolationContract, r probeReport, _ string) error {
	if r.Hostname != "fakemachine" {
		return fmt.Errorf("hostname is %q", r.Hostname)
	}

	return nil
}

func checkPrivileges(c isolationContract, r probeReport) error {
	if !c.HostKernel {
		return nil
	}
	// Anything but success is a denial, including a missing path
	for name, res := range r.Priv {
		if res == "ok" {
			return fmt.Errorf("%s succeeded", name)
		}
	}

	return nil
}

func checkDevices(c isolationContract, r probeReport) error {
	// A VM's block devices (its emulated disks, floppy, cdrom and loop
	// devices) belong to its own kernel, not the host's
	if c.HostKernel && len(r.BlockDevices) > 0 {
		return fmt.Errorf("block devices in /dev: %v", r.BlockDevices)
	}
	if c.AllowedDev == nil {
		return nil
	}
	for _, d := range r.Devices {
		if !slices.Contains(c.AllowedDev, d) {
			return fmt.Errorf("unexpected /dev/%s", d)
		}
	}

	return nil
}

func checkEnv(c isolationContract, r probeReport, leakEnv, keepEnv string) error {
	if _, ok := r.Env[leakEnv]; ok {
		return fmt.Errorf("host variable %s leaked", leakEnv)
	}
	if r.Env[keepEnv] != "1" {
		return fmt.Errorf("SetEnviron variable %s missing", keepEnv)
	}
	if c.EnvAllowlist == nil {
		return nil
	}
	for _, k := range r.EnvKeys {
		if k != keepEnv && !slices.Contains(c.EnvAllowlist, k) {
			return fmt.Errorf("unexpected variable %s", k)
		}
	}

	return nil
}

func checkIdentity(c isolationContract, r probeReport) error {
	host, _ := os.ReadFile("/etc/machine-id")
	switch {
	case r.MachineID != "" && r.MachineID == strings.TrimSpace(string(host)):
		return fmt.Errorf("machine-id is the host's")
	case c.RandomMachineID && r.MachineID == "":
		return fmt.Errorf("machine-id is empty, contract says random")
	case !c.RandomMachineID && c.HostKernel && r.MachineID != "":
		return fmt.Errorf("machine-id %q is not empty", r.MachineID)
	}
	if c.RootMapsToInvoker && os.Getuid() != 0 {
		f := strings.Fields(r.UIDMap)
		if len(f) >= 2 && f[1] == "0" && f[0] == "0" {
			return fmt.Errorf("uid 0 maps to host root: %s", r.UIDMap)
		}
	}

	return nil
}

func checkNetwork(c isolationContract, r probeReport) error {
	if r.AbstractReach != c.AbstractSocketReachable {
		return fmt.Errorf("abstract socket reachable = %v, contract says %v",
			r.AbstractReach, c.AbstractSocketReachable)
	}
	if r.LoopbackReach != c.HostLoopbackReachable {
		return fmt.Errorf("host loopback service reachable = %v, contract says %v",
			r.LoopbackReach, c.HostLoopbackReachable)
	}

	return nil
}

func checkHostIntact(_ isolationContract, r probeReport, host string, leaked bool) error {
	if leaked {
		return fmt.Errorf("a mount made in the machine is visible on the host")
	}
	if !r.LeakMounted {
		return fmt.Errorf("probe could not mount a tmpfs, propagation was not tested")
	}
	if r.SetHostnameOK {
		now, _ := os.Hostname()
		if now != host {
			return fmt.Errorf("sethostname changed the host hostname to %q", now)
		}
	}

	return nil
}

func checkSymlinks(_ isolationContract, r probeReport) error {
	if len(r.SymlinkEscape) > 0 {
		return fmt.Errorf("the host secret was read through volume symlinks %v", r.SymlinkEscape)
	}

	return nil
}

func checkKeyring(_ isolationContract, r probeReport) error {
	if r.KeyLeaked {
		return fmt.Errorf("a host key was read from the machine: %v", r.Keys)
	}

	return nil
}

func checkShm(_ isolationContract, r probeReport) error {
	if r.ShmLeaked {
		return fmt.Errorf("the host shared memory segment was read from the machine")
	}

	return nil
}

func checkHostRuntime(_ isolationContract, r probeReport) error {
	if len(r.RuntimeLeak) > 0 {
		return fmt.Errorf("host runtime files are reachable from the machine: %v", r.RuntimeLeak)
	}

	return nil
}

// There is no canary for this: the root is the machine's pivot_root in a
// mount namespace, and fakemachine has no switch for either. Dropping the
// mount namespace already stops the machine starting.
func checkRootEscape(_ isolationContract, r probeReport) error {
	if len(r.RootEscape) > 0 {
		return fmt.Errorf("the machine can read host files outside its root through %v", r.RootEscape)
	}

	return nil
}

// Writes to /sys reach the host kernel when it is shared. The machine's own
// cgroup may be delegated to the user, so making one there is only logged; with
// a private cgroup namespace the cgroup root is the machine's own too.
func checkSys(c isolationContract, r probeReport) error {
	if !c.HostKernel {
		return nil
	}
	if r.SysReadonly != c.SysReadonly {
		return fmt.Errorf("/sys read-only is %v, the contract says %v", r.SysReadonly, c.SysReadonly)
	}
	// The cgroup tree is a mount of its own, and only the machine's when the
	// cgroup namespace is private
	if !slices.Contains(c.PrivateNS, "cgroup") && !r.CgroupReadonly {
		return fmt.Errorf("the host's cgroup tree is writable")
	}
	if r.ProcSysReadonly != c.ProcSysReadonly {
		return fmt.Errorf("/proc/sys read-only is %v, the contract says %v", r.ProcSysReadonly, c.ProcSysReadonly)
	}
	own := []string{"mkdir-cgroup-own"}
	if slices.Contains(c.PrivateNS, "cgroup") {
		own = append(own, "mkdir-cgroup-root", "open-cgroup-root-procs")
	}
	var allowed []string
	for name, res := range r.Sys {
		if res == "ok" && !slices.Contains(own, name) {
			allowed = append(allowed, name)
		}
	}
	sort.Strings(allowed)
	if len(allowed) > 0 {
		return fmt.Errorf("the machine can change the host kernel through %v", allowed)
	}

	return nil
}

// A failing program must not leave anything behind. There is no canary: the
// pid namespace is what kills the leftovers, and dropping it is already
// refused (see the "no pid namespace" canary).
func TestIsolationExit(t *testing.T) {
	if InMachine() {
		t.Skip("runs on the host")
	}
	m := CreateMachine(t)
	fx := newHostFixture(t)
	before := takeSnapshot(t, fx.marker)

	code, err := m.RunInMachineWithArgs([]string{
		"-test.run", "^TestIsolationExitProbe$", "-testarg", fx.marker,
	})
	require.NoError(t, err)
	require.Equal(t, 3, code, "exit status of the program was not passed on")

	requireHostRestored(t, before, fx.marker)
	require.True(t, sleeperAlive(fx.sleeper.Process.Pid, fx.sleeperArgv), "host sleeper was killed")
}

func TestIsolation(t *testing.T) {
	if InMachine() {
		t.Skip("runs on the host")
	}
	m := CreateMachine(t)
	c := contractFor(t, m)
	fx := newHostFixture(t)
	r := runIsolationProbe(t, m, fx)

	checks := map[string]func() error{
		"namespaces": func() error { return checkHostKernelNamespaces(c, r, fx.hostNS) },
		"files":      func() error { return checkNoHostFiles(c, r) },
		"readonly":   func() error { return checkReadonlyStatic(c, r) },
		"processes":  func() error { return checkProcesses(c, r) },
		"hostname":   func() error { return checkHostname(c, r, fx.hostname) },
		"privileges": func() error { return checkPrivileges(c, r) },
		"devices":    func() error { return checkDevices(c, r) },
		"env":        func() error { return checkEnv(c, r, fx.leakEnv, fx.keepEnv) },
		"identity":   func() error { return checkIdentity(c, r) },
		"network":    func() error { return checkNetwork(c, r) },
		"host":       func() error { return checkHostIntact(c, r, fx.hostname, fx.leakSeen) },
		"symlinks":   func() error { return checkSymlinks(c, r) },
		"runtime":    func() error { return checkHostRuntime(c, r) },
		"rootescape": func() error { return checkRootEscape(c, r) },
		"sys": func() error {
			t.Logf("/sys from the machine (read-only %v, cgroup %v, /proc/sys %v): %v",
				r.SysReadonly, r.CgroupReadonly, r.ProcSysReadonly, r.Sys)

			return checkSys(c, r)
		},
		"shm": func() error {
			t.Logf("shm from the machine: %s", r.Shm)

			return checkShm(c, r)
		},
	}
	names := make([]string, 0, len(checks))
	for n := range checks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Run(n, func(t *testing.T) {
			require.NoError(t, checks[n]())
		})
	}

	// Container runtimes' default seccomp profiles refuse add_key, so there
	// may be no host key to leak
	t.Run("keyring", func(t *testing.T) {
		if len(fx.keyIDs) == 0 {
			t.Skip("no host key could be added, the keyring was not tested")
		}
		t.Logf("keyctl from the machine: %v", r.Keys)
		require.NoError(t, checkKeyring(c, r))
	})

	t.Run("fds", func(t *testing.T) {
		// Other descriptors are the Go runtime's and the test binary's, so
		// they are logged and only the host's secret is asserted
		t.Log(r.Fds)
		require.NoError(t, checkFds(c, r, fx.secretPath))
	})
}

// With SetScratch the host-backed scratch path must also be cleaned up.
func TestIsolationCleanup(t *testing.T) {
	if InMachine() {
		t.Skip("runs on the host")
	}
	m := CreateMachine(t)
	contractFor(t, m)
	fx := newHostFixture(t)
	m.SetScratch(256*1024*1024, t.TempDir())
	runIsolationProbe(t, m, fx)
}

// Two machines running at once must not see each other's processes, volumes
// or scratch, and must both clean up after themselves.
func TestIsolationConcurrent(t *testing.T) {
	if InMachine() {
		t.Skip("runs on the host")
	}
	contractFor(t, CreateMachine(t))

	for _, tc := range []struct {
		name          string
		sharedScratch bool
	}{
		{"default scratch", false},
		{"shared scratch parent", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scratchParent := t.TempDir()
			marker := randHex(t)

			type peer struct {
				m      *Machine
				dir    string
				in     peerInput
				code   int
				err    error
				report peerReport
			}
			peers := make([]*peer, 2)
			for i := range peers {
				digits := randDigits(t)
				peers[i] = &peer{
					m:   CreateMachine(t),
					dir: t.TempDir(),
					in: peerInput{
						Token:     fmt.Sprintf("%s-%d", marker, i),
						OwnDigits: digits,
						OwnArgv:   "sleep\x003600." + digits + "\x00",
					},
				}
				//nolint:gosec // Read by the machine and holds no sensitive data.
				require.NoError(t, os.WriteFile(filepath.Join(peers[i].dir, "token"),
					[]byte(peers[i].in.Token), 0o644))
			}
			for i, p := range peers {
				other := peers[1-i]
				p.in.PeerArgv = other.in.OwnArgv
				p.in.PeerHostPath = filepath.Join(other.dir, "token")
				p.m.AddVolumeAt(p.dir, isolationDir)
				if tc.sharedScratch {
					p.m.SetScratch(256*1024*1024, scratchParent)
				}
			}

			before := takeSnapshot(t, marker)

			var wg sync.WaitGroup
			for _, p := range peers {
				//nolint:musttag // Both ends share the struct, so the field names are the keys.
				input, err := json.Marshal(p.in)
				require.NoError(t, err)
				wg.Add(1)
				go func() {
					defer wg.Done()
					p.code, p.err = p.m.RunInMachineWithArgs([]string{
						"-test.run", "^TestIsolationPeerProbe$", "-testarg", string(input),
					})
				}()
			}

			// Both must be up before either looks for the other
			finished := make(chan struct{})
			go func() { wg.Wait(); close(finished) }()
			ready := func() bool {
				for _, p := range peers {
					if _, err := os.Stat(filepath.Join(p.dir, "peer-ready")); err != nil {
						return false
					}
				}

				return true
			}
			deadline := time.After(5 * time.Minute)
		wait:
			for !ready() {
				select {
				case <-finished:
					break wait
				case <-deadline:
					break wait
				case <-time.After(50 * time.Millisecond):
				}
			}
			for _, p := range peers {
				//nolint:gosec // Read by the machine and holds no sensitive data.
				require.NoError(t, os.WriteFile(filepath.Join(p.dir, "peer-go"), nil, 0o644))
			}
			<-finished

			for i, p := range peers {
				require.NoError(t, p.err, "machine %d", i)
				require.Equal(t, 0, p.code, "peer probe failed in machine %d", i)
				data, err := os.ReadFile(filepath.Join(p.dir, "peer.json"))
				require.NoError(t, err)
				//nolint:musttag // Both ends share the struct, so the field names are the keys.
				require.NoError(t, json.Unmarshal(data, &p.report))
				t.Logf("machine %d: %+v", i, p.report)

				r := p.report
				require.True(t, r.OwnSleeperSeen, "machine %d did not see its own sleeper", i)
				require.False(t, r.PeerSleeperSeen, "machine %d saw the other machine's process", i)
				require.False(t, r.PeerFileVisible, "machine %d saw the other machine's volume", i)
				require.Equal(t, p.in.Token, r.IsolationToken, "machine %d /isolation", i)
				require.Equal(t, p.in.Token, r.ScratchToken, "machine %d /scratch was changed by the other", i)
				require.Empty(t, procsWithArgv(p.in.OwnArgv), "machine %d sleeper survived", i)
			}

			after := takeSnapshot(t, marker)
			require.Empty(t, diffSnapshot(before, after), "host changed by the machine runs")
			if tc.sharedScratch {
				left, _ := os.ReadDir(scratchParent)
				require.Empty(t, left, "scratch left behind")
			}
		})
	}
}

const signalHelperEnv = "FAKEMACHINE_SIGNAL_HELPER"

// Re-exec'd by TestIsolationSignals: runs a long command in a machine.
func TestIsolationSignalHelper(t *testing.T) {
	digits := os.Getenv(signalHelperEnv)
	if digits == "" || InMachine() {
		t.Skip("only runs as a helper of TestIsolationSignals")
	}
	m := CreateMachine(t)
	_, err := m.Run("sleep 3600." + digits)
	t.Logf("machine returned: %v", err)
}

func TestIsolationSignals(t *testing.T) {
	if InMachine() {
		t.Skip("runs on the host")
	}
	m := CreateMachine(t)
	skipUnlessHostKernel(t, m)

	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(sig.String(), func(t *testing.T) {
			digits := randDigits(t)
			argv := "sleep\x003600." + digits + "\x00"
			instances := incusInstances(t)
			//nolint:noctx,gosec // Synchronous helper; it reruns this test binary with fixed arguments.
			cmd := exec.Command(os.Args[0], "-test.run", "^TestIsolationSignalHelper$",
				"-backend", backendName)
			cmd.Env = append(os.Environ(), signalHelperEnv+"="+digits)
			require.NoError(t, cmd.Start())
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				for _, p := range procsWithArgv(argv) {
					if pid, err := strconv.Atoi(p); err == nil {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})

			// The sleep in the machine, seen from the host
			require.Eventually(t, func() bool {
				return len(procsWithArgv(argv)) > 0
			}, 60*time.Second, 100*time.Millisecond, "machine did not start")

			require.NoError(t, cmd.Process.Signal(sig))
			_ = cmd.Wait()

			require.Eventually(t, func() bool {
				return len(procsWithArgv(argv)) == 0
			}, 15*time.Second, 100*time.Millisecond,
				"machine processes survived %s of fakemachine: %v", sig, procsWithArgv(argv))

			// An incus instance whose processes are gone must still be deleted
			var leftover []string
			for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
				leftover = nil
				for _, n := range incusInstances(t) {
					if !slices.Contains(instances, n) {
						leftover = append(leftover, n)
					}
				}
				if len(leftover) == 0 {
					break
				}
			}
			require.Empty(t, leftover, "incus instances survived %s of fakemachine", sig)
		})
	}
}

const ttyHelperEnv = "FAKEMACHINE_TTY_HELPER"

// Re-exec'd by TestIsolationTTY on a terminal: runs TestIsolationTTYProbe.
func TestIsolationTTYHelper(t *testing.T) {
	dir, marker, ok := strings.Cut(os.Getenv(ttyHelperEnv), ":")
	if !ok || InMachine() {
		t.Skip("only runs as a helper of TestIsolationTTY")
	}
	m := CreateMachine(t)
	m.AddVolumeAt(dir, isolationDir)
	code, err := m.RunInMachineWithArgs([]string{
		"-test.run", "^TestIsolationTTYProbe$", "-testarg", marker,
	})
	require.NoError(t, err)
	require.Equal(t, 0, code)
}

// Run the machine on a terminal the way an interactive user would, and push
// input into it from the machine with TIOCSTI. Whatever reaches the host
// terminal's input queue would be read and run by the user's shell.
func TestIsolationTTY(t *testing.T) {
	if InMachine() {
		t.Skip("runs on the host")
	}
	m := CreateMachine(t)
	skipUnlessHostKernel(t, m)
	contractFor(t, m)

	if b, err := os.ReadFile("/proc/sys/dev/tty/legacy_tiocsti"); err == nil {
		legacy := strings.TrimSpace(string(b))
		t.Logf("dev.tty.legacy_tiocsti = %s", legacy)
		if legacy == "0" {
			t.Log("the kernel denies TIOCSTI without CAP_SYS_ADMIN, so the backend is not what is tested here")
		}
	}

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	require.NoError(t, err)
	defer func() { _ = master.Close() }()
	require.NoError(t, unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0))
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	require.NoError(t, err)
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	require.NoError(t, err)
	defer func() { _ = slave.Close() }()

	// What the machine writes to the terminal, including the echo of anything
	// injected; only logged, the input queue is what matters
	var outMu sync.Mutex
	var out strings.Builder
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			outMu.Lock()
			out.Write(buf[:n])
			outMu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	marker := randHex(t)
	dir := t.TempDir()
	//nolint:noctx,gosec // Synchronous helper; it reruns this test binary with fixed arguments.
	cmd := exec.Command(os.Args[0], "-test.run", "^TestIsolationTTYHelper$", "-backend", backendName)
	cmd.Env = append(os.Environ(), ttyHelperEnv+"="+dir+":"+marker)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	require.NoError(t, cmd.Start())
	err = cmd.Wait()
	outMu.Lock()
	t.Logf("helper output:\n%s", out.String())
	outMu.Unlock()
	require.NoError(t, err)

	//nolint:gosec // The path is test-controlled.
	b, err := os.ReadFile(filepath.Join(dir, "tty.json"))
	require.NoError(t, err, "the probe did not write its report")
	t.Logf("TIOCSTI from the machine: %s", b)

	// Read back the input queue as the user's shell would. The terminal is
	// canonical and the probe ends its input with a newline, so a read returns
	// at once if anything was injected.
	// Fd() makes the descriptor blocking again, so it is only called once
	sfd := int(slave.Fd())
	require.NoError(t, unix.SetNonblock(sfd, true))
	var queued strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := unix.Read(sfd, buf)
		if n <= 0 || err != nil {
			break
		}
		queued.Write(buf[:n])
	}
	require.NotContains(t, queued.String(), marker,
		"the machine injected input into the host terminal")
}

func TestIsolationFailClosed(t *testing.T) {
	if InMachine() {
		t.Skip("runs on the host")
	}
	m := CreateMachine(t)
	skipUnlessBackend(t, m, "unshare")

	// A PATH holding only what the backend needs, minus the named tool
	pathWithout := func(t *testing.T, without string) string {
		t.Helper()
		dir := t.TempDir()
		for _, tool := range []string{"unshare", "pivot_root", "newuidmap", "newgidmap", "sh", "env"} {
			if tool == without {
				continue
			}
			if p, err := exec.LookPath(tool); err == nil {
				require.NoError(t, os.Symlink(p, filepath.Join(dir, tool)))
			}
		}

		return dir
	}

	for _, tool := range []string{"pivot_root", "unshare"} {
		t.Run("missing "+tool, func(t *testing.T) {
			t.Setenv("PATH", pathWithout(t, tool))
			_, err := NewMachineWithBackend("unshare")
			require.Error(t, err, "backend must refuse to run without %s", tool)
		})
	}
}

// Weaken the sandbox on purpose and check the matching contract check notices.
func TestIsolationCanary(t *testing.T) {
	if InMachine() {
		t.Skip("runs on the host")
	}
	m := CreateMachine(t)
	skipUnlessBackend(t, m, "unshare")
	c := contracts["unshare"]

	dropArg := func(arg string) func(t *testing.T) {
		return func(t *testing.T) {
			t.Helper()
			// The probed args are cached, so forget them on both sides
			resetProbe := func() {
				unshareIDMap.once = sync.Once{}
				unshareIDMap.args, unshareIDMap.err = nil, nil
			}
			orig := unshareArgs
			t.Cleanup(resetProbe)
			t.Cleanup(func() { unshareArgs = orig })
			resetProbe()
			unshareArgs = slices.DeleteFunc(slices.Clone(orig), func(a string) bool { return a == arg })
		}
	}

	canaries := []struct {
		name   string
		weaken func(t *testing.T)
		check  func(r probeReport, fx *hostFixture) error // nil: the machine must not start
	}{
		// Mounting a fresh /proc needs the pid namespace, so the backend must
		// refuse to start rather than run with the host's processes visible
		{"no pid namespace", dropArg("--pid"), nil},
		{"no ipc namespace", dropArg("--ipc"), func(r probeReport, _ *hostFixture) error {
			return checkShm(c, r)
		}},
		// No uts or mount propagation canaries: without the uts namespace the
		// setup script would rename the real host, and shared propagation
		// breaks pivot_root
		{"writable static volumes", func(t *testing.T) {
			t.Helper()
			unshareReadonlyStatic = false
			t.Cleanup(func() { unshareReadonlyStatic = true })
		}, func(r probeReport, _ *hostFixture) error { return checkReadonlyStatic(c, r) }},
		{"inherited fds", func(t *testing.T) {
			t.Helper()
			unshareCloseFds = false
			t.Cleanup(func() { unshareCloseFds = true })
		}, func(r probeReport, fx *hostFixture) error { return checkFds(c, r, fx.secretPath) }},
		{"inherited session keyring", func(t *testing.T) {
			t.Helper()
			unshareNewSessionKeyring = false
			t.Cleanup(func() { unshareNewSessionKeyring = true })
		}, func(r probeReport, fx *hostFixture) error {
			if len(fx.keyIDs) == 0 {
				return fmt.Errorf("no host keys to leak")
			}

			return checkKeyring(c, r)
		}},
		// Sharing the host's /dev/shm is the only way for its runtime files to
		// show up, and volumes are mounted after the machine's own /dev/shm
		{"host /dev/shm volume", func(_ *testing.T) {}, func(r probeReport, _ *hostFixture) error {
			return checkHostRuntime(c, r)
		}},
		{"writable kernel filesystems", func(t *testing.T) {
			t.Helper()
			unshareReadonlyKernelFS = false
			t.Cleanup(func() { unshareReadonlyKernelFS = true })
		}, func(r probeReport, _ *hostFixture) error {
			if err := checkPrivileges(c, r); err != nil {
				return err
			}

			return checkSys(c, r)
		}},
	}

	// Canaries which weaken the sandbox by configuring the machine
	machineHooks := map[string]func(m *Machine){
		"host /dev/shm volume": func(m *Machine) { m.AddVolumeAt("/dev/shm", "/dev/shm") },
	}

	for _, cn := range canaries {
		t.Run(cn.name, func(t *testing.T) {
			cn.weaken(t)
			m := CreateMachine(t)
			if hook := machineHooks[cn.name]; hook != nil {
				hook(m)
			}
			if cn.check == nil {
				_, err := m.RunInMachineWithArgs([]string{"-test.run", "^TestIsolationProbe$", "-testarg", "{}"})
				require.Error(t, err, "machine started with a weakened sandbox")

				return
			}
			fx := newHostFixture(t)
			r := runIsolationProbe(t, m, fx)
			require.Error(t, cn.check(r, fx), "weakened sandbox went unnoticed")
		})
	}
}

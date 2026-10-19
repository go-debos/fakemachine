package fakemachine

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Mount point of the volume shared between the host and the machine.
const isolationDir = "/isolation"

// What the host tells the probe about its own state.
type probeInput struct {
	Marker         string // random, appears in host-only files, args and mounts
	SecretPath     string // host file outside every volume
	SleeperPID     int    // host process which must not be visible
	SleeperCmdline string // its exact /proc cmdline; JSON escapes the NULs so the probe's own argv can't match
	AbstractSocket string // host abstract unix socket, without the leading @
	LeakEnv        string // name of a host variable which must not appear
	KeepEnv        string // name of a SetEnviron variable which must appear
	// Files the probe creates in the machine which must not appear on the host
	HostAbsent []string
	// Symlinks in the /isolation volume pointing at the secret
	Symlinks []string
	// Host keys holding the marker, and their description
	KeyIDs  []int
	KeyDesc string
	// SysV shared memory key of a host segment holding the marker
	ShmKey int
}

// What the probe observed from inside; the host judges it against the contract.
type probeReport struct {
	Hostname         string
	NS               map[string]string // namespace name -> link target
	SecretVisible    bool
	StaticReadonly   bool
	IsolationWritten bool
	SleeperInProc    bool // some /proc/*/cmdline is the host sleeper's
	Priv             map[string]string
	Devices          []string
	BlockDevices     []string
	Fds              []string
	EnvKeys          []string
	Env              map[string]string
	MachineID        string
	UIDMap           string
	AbstractReach    bool
	SetHostnameOK    bool
	LeakMounted      bool
	SymlinkEscape    []string          // symlinks through which the secret was read
	Keys             map[string]string // keyctl attempt -> outcome
	KeyLeaked        bool              // some key read back holds the marker
	Shm              string            // outcome of looking up the host segment
	ShmLeaked        bool              // the segment was attached and holds the marker
	Sys              map[string]string // /sys and cgroup attempt -> outcome
	SysReadonly      bool
	CgroupReadonly   bool
	ProcSysReadonly  bool
}

func mountReadonly(p string) bool {
	var st syscall.Statfs_t

	return syscall.Statfs(p, &st) == nil && st.Flags&1 != 0 // ST_RDONLY
}

// Look up the host's shared memory segment by key and read it. The segment is
// mode 0666, so only the IPC namespace can keep it from the machine.
func probeShm(key int, marker string) (string, bool) {
	id, err := unix.SysvShmGet(key, 0, 0)
	if err != nil {
		return "get: " + errName(err), false
	}
	b, err := unix.SysvShmAttach(id, 0, unix.SHM_RDONLY)
	if err != nil {
		return "attach: " + errName(err), false
	}
	defer func() { _ = unix.SysvShmDetach(b) }()

	return "ok", strings.HasPrefix(string(b), marker)
}

// Try to change the host kernel through /sys and the cgroup tree. Files are
// only opened for writing, never written, and directories made are removed.
//
//nolint:nonamedreturns // Names document the two results.
func probeSys(marker string) (res map[string]string, readonly bool) {
	res = map[string]string{}
	open := func(name, p string) {
		//nolint:gosec // The probe deliberately opens paths chosen by the test.
		f, err := os.OpenFile(p, os.O_WRONLY, 0)
		if err == nil {
			_ = f.Close()
		}
		res[name] = errName(err)
	}
	open("open-thp", "/sys/kernel/mm/transparent_hugepage/enabled")
	open("open-power-state", "/sys/power/state")
	open("open-lo-mtu", "/sys/class/net/lo/mtu")
	params, _ := filepath.Glob("/sys/module/*/parameters/*")
	res["open-module-param"] = "none found"
	for _, p := range params {
		if info, err := os.Stat(p); err == nil && info.Mode().Perm()&0o222 != 0 {
			open("open-module-param", p)

			break
		}
	}

	readonly = mountReadonly("/sys")

	mkdir := func(name, dir string) {
		p := filepath.Join(dir, "fm-probe-"+marker)
		//nolint:gosec // The directory is removed straight away; its mode is irrelevant.
		err := os.Mkdir(p, 0o755)
		if err == nil {
			//nolint:gosec // p was just created by the probe.
			_ = os.Remove(p)
		}
		res[name] = errName(err)
	}
	mkdir("mkdir-cgroup-root", "/sys/fs/cgroup")
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		// cgroup v2: "0::/path"
		if _, own, ok := strings.Cut(strings.TrimSpace(string(b)), "::"); ok && own != "/" {
			mkdir("mkdir-cgroup-own", filepath.Join("/sys/fs/cgroup", own))
		}
	}
	open("open-cgroup-root-procs", "/sys/fs/cgroup/cgroup.procs")

	return res, readonly
}

// Read the secret through symlinks the host left in a volume.
func probeSymlinks(links []string, marker string) []string {
	var escaped []string
	for _, l := range links {
		//nolint:gosec // The probe deliberately reads paths chosen by the test.
		if b, err := os.ReadFile(l); err == nil && string(b) == marker {
			escaped = append(escaped, l)
		}
	}

	return escaped
}

// Look for the host's keys by serial and by description. A VM kernel may
// have unrelated keys with the same serials, so only the marker counts.
func probeKeys(ids []int, desc, marker string) (map[string]string, bool) {
	res := map[string]string{}
	leaked := false
	read := func(name string, id int) {
		buf := make([]byte, 256)
		n, err := unix.KeyctlBuffer(unix.KEYCTL_READ, id, buf, 0)
		res["read-"+name] = errName(err)
		if err == nil && n <= len(buf) && string(buf[:n]) == marker {
			leaked = true
		}
	}
	for _, id := range ids {
		name := strconv.Itoa(id)
		_, err := unix.KeyctlString(unix.KEYCTL_DESCRIBE, id)
		res["describe-"+name] = errName(err)
		read(name, id)
	}
	for name, ring := range map[string]int{
		"session": unix.KEY_SPEC_SESSION_KEYRING,
		"user":    unix.KEY_SPEC_USER_KEYRING,
	} {
		id, err := unix.KeyctlSearch(ring, "user", desc, 0)
		res["search-"+name] = errName(err)
		if err == nil {
			read("found-"+name, id)
		}
	}

	return res, leaked
}

func errName(err error) string {
	if err == nil {
		return "ok"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}

	return err.Error()
}

func probeNS() map[string]string {
	ns := map[string]string{}
	for _, n := range []string{"user", "mnt", "pid", "uts", "ipc", "net", "cgroup"} {
		ns[n], _ = os.Readlink("/proc/self/ns/" + n)
	}

	return ns
}

func probeSleeperInProc(cmdline string) bool {
	entries, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, e := range entries {
		//nolint:gosec // The probe deliberately reads paths chosen by the test.
		if b, err := os.ReadFile(e); err == nil && string(b) == cmdline {
			return true
		}
	}

	return false
}

func probePrivileges() map[string]string {
	res := map[string]string{}

	f, err := os.OpenFile("/dev/loop-control", os.O_RDWR, 0)
	if err == nil {
		_ = f.Close()
	}
	res["open-loop-control"] = errName(err)

	node := "/scratch/fm-probe-blk"
	err = syscall.Mknod(node, syscall.S_IFBLK|0o600, 7<<8)
	if err == nil {
		_ = os.Remove(node)
	}
	res["mknod-block"] = errName(err)

	_, _, e := syscall.Syscall(syscall.SYS_INIT_MODULE, 0, 0, 0)
	if e == 0 {
		res["init-module"] = "ok"
	} else {
		res["init-module"] = e.Error()
	}

	// Only opened, never written to
	for name, p := range map[string]string{
		"open-drop-caches":   "/proc/sys/vm/drop_caches",
		"open-sysrq-trigger": "/proc/sysrq-trigger",
		"open-uevent-helper": "/sys/kernel/uevent_helper",
	} {
		f, err := os.OpenFile(p, os.O_WRONLY, 0)
		if err == nil {
			_ = f.Close()
		}
		res[name] = errName(err)
	}

	_ = os.MkdirAll("/scratch/fm-probe-mnt", 0o755)
	err = syscall.Mount("/scratch/fm-probe-none", "/scratch/fm-probe-mnt", "ext4", 0, "")
	if err == nil {
		_ = syscall.Unmount("/scratch/fm-probe-mnt", 0)
	}
	res["mount-ext4"] = errName(err)
	_ = os.Remove("/scratch/fm-probe-mnt")

	path, _ := syscall.BytePtrFromString("/scratch/fm-probe-none")
	_, _, e = syscall.Syscall(syscall.SYS_SWAPON, uintptr(unsafe.Pointer(path)), 0, 0)
	if e == 0 {
		res["swapon"] = "ok"
	} else {
		res["swapon"] = e.Error()
	}

	return res
}

//nolint:nonamedreturns // Names document the two results.
func probeDevices() (top, block []string) {
	entries, _ := os.ReadDir("/dev")
	for _, e := range entries {
		top = append(top, e.Name())
	}
	_ = filepath.WalkDir("/dev", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			//nolint:nilerr // Unreadable entries are skipped, not fatal.
			return nil
		}
		if info, err := d.Info(); err == nil && info.Mode()&os.ModeDevice != 0 &&
			info.Mode()&os.ModeCharDevice == 0 {
			block = append(block, p)
		}

		return nil
	})

	return top, block
}

func probeFds() []string {
	var fds []string
	dir, err := os.Open("/proc/self/fd")
	if err != nil {
		return nil
	}
	defer func() { _ = dir.Close() }()
	names, _ := dir.Readdirnames(-1)
	for _, n := range names {
		target, err := os.Readlink("/proc/self/fd/" + n)
		if err != nil {
			continue // the directory fd itself
		}
		if n == "0" || n == "1" || n == "2" {
			continue
		}
		fds = append(fds, n+" -> "+target)
	}

	return fds
}

// Mount something with a recognisable source and let the host check whether
// it can see it.
func probeMountPropagation(marker string) bool {
	//nolint:gosec // The mount point only needs to exist inside the machine.
	_ = os.MkdirAll("/scratch/leak", 0o755)
	if err := syscall.Mount("fm-leak-"+marker, "/scratch/leak", "tmpfs", 0, ""); err != nil {
		return false
	}
	defer func() { _ = syscall.Unmount("/scratch/leak", 0) }()

	//nolint:gosec // The file is read by the host and holds no sensitive data.
	_ = os.WriteFile(filepath.Join(isolationDir, "ready"), nil, 0o644)
	for i := 0; i < 100 && !probeHostWrote("ack"); i++ {
		time.Sleep(50 * time.Millisecond)
	}

	return true
}

// Whether the host has created name in /isolation. The VM backends mount it
// with 9p cache=loose, where a lookup can keep returning a cached ENOENT for a
// file the host creates later, but a readdir always asks the host.
func probeHostWrote(name string) bool {
	entries, _ := os.ReadDir(isolationDir)
	for _, e := range entries {
		if e.Name() == name {
			return true
		}
	}

	return false
}

// TestIsolationTTYProbe runs inside the machine, started by TestIsolationTTY.
// It pushes the marker into the input of its terminal, which is the host's
// if the backend passes it straight through.
func TestIsolationTTYProbe(t *testing.T) {
	if !InMachine() {
		t.Skip("runs inside the machine, started by TestIsolationTTY")
	}
	inject := func(fd uintptr) string {
		for _, b := range []byte(testArg + "\n") {
			//nolint:gosec // TIOCSTI takes a pointer to the byte to inject.
			_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSTI, uintptr(unsafe.Pointer(&b)))
			if e != 0 {
				return e.Error()
			}
		}

		return "ok"
	}
	res := map[string]string{"stdin": inject(0)}
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		res["dev-tty"] = inject(f.Fd())
		_ = f.Close()
	} else {
		res["dev-tty"] = "open: " + errName(err)
	}
	out, err := json.Marshal(res)
	require.NoError(t, err)
	//nolint:gosec // The report is read by the host and holds no sensitive data.
	require.NoError(t, os.WriteFile(filepath.Join(isolationDir, "tty.json"), out, 0o644))
}

// What one of two concurrent machines is told about itself and its peer.
type peerInput struct {
	Token        string // written to its /scratch and found in its /isolation
	OwnDigits    string // its sleeper runs sleep 3600.<digits>
	OwnArgv      string
	PeerArgv     string // the peer's sleeper cmdline
	PeerHostPath string // host path of a file in the peer's /isolation
}

type peerReport struct {
	OwnSleeperSeen  bool // positive control for PeerSleeperSeen
	PeerSleeperSeen bool
	PeerFileVisible bool
	IsolationToken  string // /isolation/token as seen by this machine
	ScratchToken    string // /scratch/fm-peer read back after both wrote theirs
}

// TestIsolationPeerProbe runs in each of the two machines started by
// TestIsolationConcurrent. Both start a sleeper and write to /scratch, then
// wait for the host so they observe each other while both are running.
func TestIsolationPeerProbe(t *testing.T) {
	if !InMachine() {
		t.Skip("runs inside the machine, started by TestIsolationConcurrent")
	}
	var in peerInput
	//nolint:musttag // Both ends share the struct, so the field names are the keys.
	require.NoError(t, json.Unmarshal([]byte(testArg), &in))

	//nolint:noctx,gosec // The sleeper is killed by the deferred cleanup; its arguments are fixed apart from the digits.
	sleeper := exec.Command("sleep", "3600."+in.OwnDigits)
	require.NoError(t, sleeper.Start())
	defer func() {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
	}()
	require.Eventually(t, func() bool { return probeSleeperInProc(in.OwnArgv) },
		10*time.Second, 10*time.Millisecond, "own sleeper did not start")

	scratch := filepath.Join("/scratch", "fm-peer")
	//nolint:gosec // The token holds no sensitive data.
	require.NoError(t, os.WriteFile(scratch, []byte(in.Token), 0o644))

	//nolint:gosec // The file is read by the host and holds no sensitive data.
	require.NoError(t, os.WriteFile(filepath.Join(isolationDir, "peer-ready"), nil, 0o644))
	require.Eventually(t, func() bool { return probeHostWrote("peer-go") },
		5*time.Minute, 50*time.Millisecond, "the host never said go")

	r := peerReport{
		OwnSleeperSeen:  probeSleeperInProc(in.OwnArgv),
		PeerSleeperSeen: probeSleeperInProc(in.PeerArgv),
	}
	_, err := os.Stat(in.PeerHostPath)
	r.PeerFileVisible = err == nil
	if b, err := os.ReadFile(filepath.Join(isolationDir, "token")); err == nil {
		r.IsolationToken = string(b)
	}
	//nolint:gosec // The probe deliberately reads paths chosen by the test.
	if b, err := os.ReadFile(scratch); err == nil {
		r.ScratchToken = string(b)
	}

	//nolint:musttag // Both ends share the struct, so the field names are the keys.
	out, err := json.Marshal(r)
	require.NoError(t, err)
	//nolint:gosec // The report is read by the host and holds no sensitive data.
	require.NoError(t, os.WriteFile(filepath.Join(isolationDir, "peer.json"), out, 0o644))
}

// TestIsolationProbe is not a test by itself: it is the part of the isolation
// suite which runs inside the machine, observes it and writes a report for the
// host to check against the backend's contract.
func TestIsolationProbe(t *testing.T) {
	if !InMachine() {
		t.Skip("runs inside the machine, started by TestIsolation")
	}

	var in probeInput
	//nolint:musttag // Both ends share the struct, so the field names are the keys.
	require.NoError(t, json.Unmarshal([]byte(testArg), &in))

	r := probeReport{
		NS:   probeNS(),
		Priv: probePrivileges(),
		Env:  map[string]string{},
	}
	r.Hostname, _ = os.Hostname()

	_, err := os.Stat(in.SecretPath)
	r.SecretVisible = err == nil

	var st syscall.Statfs_t
	if err := syscall.Statfs("/usr", &st); err == nil {
		r.StaticReadonly = st.Flags&1 != 0 // ST_RDONLY
	}

	//nolint:gosec // The file is read by the host and holds no sensitive data.
	r.IsolationWritten = os.WriteFile(filepath.Join(isolationDir, "written"), []byte(in.Marker), 0o644) == nil
	for _, p := range in.HostAbsent {
		//nolint:gosec // The probe deliberately writes to paths chosen by the test.
		_ = os.WriteFile(p, []byte(in.Marker), 0o644)
	}

	r.SleeperInProc = probeSleeperInProc(in.SleeperCmdline)
	r.Devices, r.BlockDevices = probeDevices()
	r.Fds = probeFds()

	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		r.EnvKeys = append(r.EnvKeys, k)
		if k == in.LeakEnv || k == in.KeepEnv {
			r.Env[k] = v
		}
	}

	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		r.MachineID = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/self/uid_map"); err == nil {
		r.UIDMap = strings.TrimSpace(string(b))
	}

	//nolint:noctx // Synchronous probe; the timeout bounds the attempt.
	if c, err := net.DialTimeout("unix", "@"+in.AbstractSocket, time.Second); err == nil {
		r.AbstractReach = true
		_ = c.Close()
	}

	// Changing the hostname must stay inside the machine
	orig := r.Hostname
	r.SetHostnameOK = syscall.Sethostname([]byte("fm-probe")) == nil
	if r.SetHostnameOK {
		_ = syscall.Sethostname([]byte(orig))
	}

	r.LeakMounted = probeMountPropagation(in.Marker)
	r.SymlinkEscape = probeSymlinks(in.Symlinks, in.Marker)
	r.Keys, r.KeyLeaked = probeKeys(in.KeyIDs, in.KeyDesc, in.Marker)
	r.Shm, r.ShmLeaked = probeShm(in.ShmKey, in.Marker)
	r.Sys, r.SysReadonly = probeSys(in.Marker)
	r.CgroupReadonly = mountReadonly("/sys/fs/cgroup")
	r.ProcSysReadonly = mountReadonly("/proc/sys")

	//nolint:musttag // Both ends share the struct, so the field names are the keys.
	out, err := json.Marshal(r)
	require.NoError(t, err)
	//nolint:gosec // The report is read by the host and holds no sensitive data.
	require.NoError(t, os.WriteFile(filepath.Join(isolationDir, "report.json"), out, 0o644))
}

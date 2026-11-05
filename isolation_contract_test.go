package fakemachine

import "testing"

// isolationContract is what a backend promises (and, where it does not, what
// is known to be shared with the host). Both directions are asserted, so a
// change in isolation, better or worse, has to be made here on purpose.
type isolationContract struct {
	// The machine runs on the host kernel; the VM backends get their own
	// kernel so most namespace and privilege checks do not apply
	HostKernel bool
	// Namespaces which must differ from the host's
	PrivateNS []string
	// Namespaces known to be shared with the host
	SharedNS []string
	// The host's abstract unix sockets can be reached from the machine
	AbstractSocketReachable bool
	// Services on the host's loopback interface can be reached; the VM
	// backends' user-mode networking does not forward it
	HostLoopbackReachable bool
	// Static volumes (/usr etc) are mounted read-only
	ReadonlyStatic bool
	// Top level entries allowed in /dev; nil disables the check
	AllowedDev []string
	// Environment variable names allowed in the job besides the machine's
	// SetEnviron ones; nil disables the check
	EnvAllowlist []string
	// The machine's root is the invoking user, so is only host root when the
	// test runs as root
	RootMapsToInvoker bool
	// /sys is mounted read-only
	SysReadonly bool
	// /proc/sys is mounted read-only
	ProcSysReadonly bool
	// /etc/machine-id is a random value for each run rather than empty
	RandomMachineID bool
}

var hostKernelEnv = []string{
	"HOME", "PATH", "IN_FAKE_MACHINE", "FAKEMACHINE_BACKEND", "TERM",
	// exported by some /bin/sh implementations
	"PWD", "OLDPWD", "SHLVL", "_",
}

var contracts = map[string]isolationContract{
	"unshare": {
		HostKernel:              true,
		PrivateNS:               []string{"user", "mnt", "pid", "uts", "ipc"},
		SharedNS:                []string{"net", "cgroup"},
		AbstractSocketReachable: true,
		HostLoopbackReachable:   true,
		ReadonlyStatic:          true,
		AllowedDev: []string{
			"null", "zero", "full", "random", "urandom", "tty",
			"pts", "shm", "ptmx", "fd", "stdin", "stdout", "stderr",
		},
		EnvAllowlist:      hostKernelEnv,
		RootMapsToInvoker: true,
		SysReadonly:       true,
		ProcSysReadonly:   true,
	},
	"incus": {
		HostKernel: true,
		PrivateNS:  []string{"user", "mnt", "pid", "uts", "ipc", "net", "cgroup"},
		// An incus container has its own network namespace, so the host's
		// abstract sockets are unreachable
		AbstractSocketReachable: false,
		ReadonlyStatic:          true,
		SysReadonly:             true,
		RootMapsToInvoker:       true,
		RandomMachineID:         true,
	},
	// The VM backends have their own kernel, so only the filesystem, process
	// and environment views are asserted
	"kvm":  {},
	"qemu": {},
}

func contractFor(t *testing.T, m *Machine) isolationContract {
	t.Helper()
	c, ok := contracts[m.backend.Name()]
	if !ok {
		t.Skipf("no isolation contract for the %s backend", m.backend.Name())
	}

	return c
}

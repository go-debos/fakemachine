//go:build linux && (arm64 || amd64)

package fakemachine

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

type qemuBackend struct {
	machine *Machine
}

func newQemuBackend(m *Machine) qemuBackend {
	return qemuBackend{machine: m}
}

func (b qemuBackend) Name() string {
	return "qemu"
}

func (b qemuBackend) Supported() (bool, error) {
	if _, err := b.QemuPath(); err != nil {
		return false, err
	}

	if _, err := b.VirtiofsdPath(); err != nil {
		return false, err
	}

	return true, nil
}

type qemuMachine struct {
	binary  string
	console string
	machine string
	/* Cpu to use for qemu backend if the architecture doesn't have a good default */
	qemuCPU string
}

var qemuMachines = map[Arch]qemuMachine{
	Amd64: {
		binary:  "qemu-system-x86_64",
		console: "ttyS0",
		machine: "pc",
	},
	Arm64: {
		binary:  "qemu-system-aarch64",
		console: "ttyAMA0",
		machine: "virt",
		/* The default cpu is a 32 bit one, which isn't very useful
		 * for 64 bit arm. There is no cpu setting for "minimal" 64
		 * bit linux capable processor. The only generic setting
		 * is "max", but that can be very slow to emulate. So pick
		 * a specific small cortex-a processor instead */
		qemuCPU: "cortex-a53",
	},
}

func (b qemuBackend) QemuPath() (string, error) {
	machine, ok := qemuMachines[b.machine.arch]
	if !ok {
		return "", fmt.Errorf("unsupported arch for qemu: %s", b.machine.arch)
	}

	path, err := exec.LookPath(machine.binary)
	if err != nil {
		return "", fmt.Errorf("failed to find qemu binary %s: %w", machine.binary, err)
	}

	return path, nil
}

/* virtiofsd is usually installed outside of PATH, so check the locations
 * used by the distributions first. */
var virtiofsdPaths = []string{
	"/usr/libexec/virtiofsd",
	"/usr/lib/virtiofsd",
}

func (b qemuBackend) VirtiofsdPath() (string, error) {
	for _, p := range virtiofsdPaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	path, err := exec.LookPath("virtiofsd")
	if err != nil {
		return "", fmt.Errorf("failed to find virtiofsd binary: %w", err)
	}

	return path, nil
}

func (b qemuBackend) KernelRelease() (string, error) {
	/* First try the kernel the current system is running, but if there are no
	 * modules for that try the latest from /lib/modules. The former works best
	 * for systems directly running fakemachine, the latter makes sense in docker
	 * environments */
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", fmt.Errorf("failed to get kernel release: %w", err)
	}

	n := bytes.IndexByte(u.Release[:], 0)
	if n < 0 {
		n = len(u.Release)
	}
	release := string(u.Release[:n])

	moduleDir := path.Join("/lib/modules", release)
	if _, err := os.Stat(moduleDir); err == nil {
		return release, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("checking module directory %s: %w", moduleDir, err)
	}

	files, err := os.ReadDir("/lib/modules")
	if err != nil {
		return "", fmt.Errorf("listing /lib/modules: %w", err)
	}

	for _, v := range slices.Backward(files) {
		/* Ensure the kernel name starts with a digit, in order
		 * to filter out 'extramodules-ARCH' on ArchLinux */
		filename := v.Name()
		if len(filename) > 0 && filename[0] >= '0' && filename[0] <= '9' {
			return filename, nil
		}
	}

	return "", fmt.Errorf("kernel not found")
}

func (b qemuBackend) KernelPath() (string, error) {
	/* First we look within the modules directory, as supported by
	 * various distributions - Arch, Fedora...
	 *
	 * ... perhaps because systemd requires it to allow hibernation
	 * https://github.com/systemd/systemd/commit/edda44605f06a41fb86b7ab8128dcf99161d2344
	 */
	moddir, err := b.ModulePath()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("failed to get kernel module path: %w", err)
	}
	if err == nil {
		kernelPath := path.Join(moddir, "vmlinuz")
		if _, err := os.Stat(kernelPath); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("failed to stat kernel path %s: %w", kernelPath, err)
			}
		} else {
			return kernelPath, nil
		}
	}

	/* Fall-back to the previous method and look in /boot */
	kernelRelease, err := b.KernelRelease()
	if err != nil {
		return "", err
	}

	kernelPath := path.Join("/boot", "vmlinuz-"+kernelRelease)
	if _, err := os.Stat(kernelPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("kernel not found at %s", kernelPath)
		}

		return "", fmt.Errorf("failed to stat kernel path %s: %w", kernelPath, err)
	}

	return kernelPath, nil
}

func (b qemuBackend) ModulePath() (string, error) {
	kernelRelease, err := b.KernelRelease()
	if err != nil {
		return "", err
	}

	moddir := "/lib/modules"
	if b.machine.mergedUsr {
		moddir = "/usr/lib/modules"
	}

	moddir = path.Join(moddir, kernelRelease)
	if _, err := os.Stat(moddir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("module directory not found at %s: %w", moddir, err)
		}

		return "", fmt.Errorf("stat %s: %w", moddir, err)
	}

	return moddir, nil
}

func (b qemuBackend) UdevRules() []string {
	udevRules := []string{}

	// create symlink under /dev/disk/by-fakemachine-label/ for each virtual image
	for i, img := range b.machine.images {
		suffix := diskSuffix(i)
		udevRules = append(udevRules,
			fmt.Sprintf(`KERNEL=="vd%s", SYMLINK+="disk/by-fakemachine-label/%s"`, suffix, img.label),
			fmt.Sprintf(`KERNEL=="vd%s[0-9]*", SYMLINK+="disk/by-fakemachine-label/%s-part%%n"`, suffix, img.label))
	}

	return udevRules
}

func (b qemuBackend) JobOutputTTY() string {
	// By default we send job output to the second virtio console,
	// reserving /dev/ttyS0 for boot messages (which we ignore)
	// and /dev/hvc0 for possible use by systemd as a getty
	// (which we also ignore).
	// If we are debugging, mix job output into the normal
	// console messages instead, so we can see both.
	if b.machine.showBoot {
		return "/dev/console"
	}

	return "/dev/hvc0"
}

func (b qemuBackend) MountParameters(_ mountPoint) (string, []string) {
	return "virtiofs", []string{"defaults"}
}

func (b qemuBackend) InitModules() []string {
	return []string{"virtio_pci", "virtio_console", "virtiofs"}
}

func (b qemuBackend) InitStaticVolumes() []mountPoint {
	return []mountPoint{}
}

func (b qemuBackend) Start() (bool, error) {
	return b.StartQemu(false)
}

// startVirtiofsd starts a virtiofsd process sharing the mount point's host
// directory on a socket created in socketDir. The listening socket is
// created before virtiofsd is started, so qemu can connect to it straight
// away. The returned command may be non-nil even on error, in which case
// the virtiofsd process was started and still needs to be stopped.
func (b qemuBackend) startVirtiofsd(virtiofsd string, socketDir string, point mountPoint) (_ *exec.Cmd, _ string, err error) {
	socketPath := path.Join(socketDir, point.label+".sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, "", fmt.Errorf("failed to create virtiofsd socket %s: %w", socketPath, err)
	}
	l.SetUnlinkOnClose(false)
	defer func() {
		if closeErr := l.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close virtiofsd socket %s: %w", socketPath, closeErr))
		}
	}()

	f, err := l.File()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get virtiofsd socket file: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close virtiofsd socket file %s: %w", socketPath, closeErr))
		}
	}()

	args := []string{
		"--fd", "3",
		"--shared-dir", point.hostDirectory,
		"--sandbox", "none",
		"--log-level", "error",
		/* File handles need CAP_DAC_READ_SEARCH, which e.g. docker
		 * containers lack; since virtiofsd 1.14 root defaults to
		 * "prefer", which fails to start without it */
		"--inode-file-handles=never",
		/* The host directories aren't expected to change behind the
		 * guest's back, so cache aggressively like 9p's cache=loose did;
		 * the default revalidation makes booting much slower without kvm */
		"--cache=always",
	}

	/* An unprivileged virtiofsd can't change file ownership, squash all
	 * guest ids to our own so e.g. chown in the guest doesn't fail */
	if uid := os.Getuid(); uid != 0 {
		args = append(args,
			"--translate-uid", fmt.Sprintf("squash-guest:0:%d:4294967295", uid),
			"--translate-gid", fmt.Sprintf("squash-guest:0:%d:4294967295", os.Getgid()))
	}

	//nolint:noctx,gosec // virtiofsd is found by VirtiofsdPath and no shell is invoked.
	cmd := exec.Command(virtiofsd, args...)
	cmd.ExtraFiles = []*os.File{f}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("failed to start virtiofsd for %s: %w", point.hostDirectory, err)
	}

	return cmd, socketPath, nil
}

func (b qemuBackend) StartQemu(kvm bool) (_ bool, err error) {
	m := b.machine
	qemuMachine := qemuMachines[m.arch]

	kernelPath, err := b.KernelPath()
	if err != nil {
		return false, err
	}
	memory := fmt.Sprintf("%d", m.memory)
	numcpus := fmt.Sprintf("%d", m.numcpus)
	qemuargs := []string{
		qemuMachine.binary,
		"-smp", numcpus,
		"-m", memory,
		"-kernel", kernelPath,
		"-initrd", m.initrdpath,
		"-display", "none",
		"-nic", "user,model=virtio-net-pci",
		"-no-reboot",
		// virtiofs requires the guest memory to be shared with virtiofsd
		"-object", fmt.Sprintf("memory-backend-memfd,id=mem,size=%dM,share=on", m.memory),
	}

	if kvm {
		qemuargs = append(qemuargs,
			"-cpu", "host",
			"-enable-kvm")
	} else if qemuMachine.qemuCPU != "" {
		qemuargs = append(qemuargs, "-cpu", qemuMachine.qemuCPU)
	}

	qemuargs = append(qemuargs, "-machine", qemuMachine.machine+",memory-backend=mem")
	console := fmt.Sprintf("console=%s", qemuMachine.console)
	kernelargs := []string{
		console, "panic=-1",
		"plymouth.enable=0",
		"systemd.unit=fakemachine.service",
	}

	if m.showBoot {
		// Create a character device representing our stdio
		// file descriptors, and connect the emulated serial
		// port (which is the console device for the BIOS,
		// Linux and systemd, and is also connected to the
		// fakemachine script) to that device
		qemuargs = append(qemuargs,
			"-chardev", "stdio,id=for-ttyS0,signal=off",
			"-serial", "chardev:for-ttyS0")
		kernelargs = append(kernelargs, "loglevel=7")
	} else {
		qemuargs = append(qemuargs,
			// Create the bus for virtio consoles
			"-device", "virtio-serial",
			// Create /dev/ttyS0 to be the VM console, but
			// ignore anything written to it, so that it
			// doesn't corrupt our terminal
			"-chardev", "null,id=for-ttyS0",
			"-serial", "chardev:for-ttyS0",
			// Connect the fakemachine script to our stdio
			// file descriptors
			"-chardev", "stdio,id=for-hvc0,signal=off",
			"-device", "virtconsole,chardev=for-hvc0")
	}

	virtiofsd, err := b.VirtiofsdPath()
	if err != nil {
		return false, err
	}

	socketDir, err := os.MkdirTemp("", "fakemachine-virtiofsd-")
	if err != nil {
		return false, fmt.Errorf("failed to create virtiofsd socket directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(socketDir); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to remove virtiofsd socket directory %s: %w", socketDir, removeErr))
		}
	}()

	// virtiofsd exits once qemu disconnects; make sure it also goes
	// away if qemu fails to start
	var daemons []*exec.Cmd
	defer func() {
		for _, d := range daemons {
			_ = d.Process.Kill()
			_ = d.Wait()
		}
	}()

	for _, point := range m.mounts {
		daemon, socketPath, err := b.startVirtiofsd(virtiofsd, socketDir, point)
		if daemon != nil {
			daemons = append(daemons, daemon)
		}
		if err != nil {
			return false, err
		}

		qemuargs = append(qemuargs,
			"-chardev", fmt.Sprintf("socket,id=char-%s,path=%s", point.label, socketPath),
			"-device", fmt.Sprintf("vhost-user-fs-pci,chardev=char-%s,tag=%s", point.label, point.label))
	}

	for i, img := range m.images {
		qemuargs = append(qemuargs, "-drive",
			fmt.Sprintf("file=%s,if=none,format=raw,cache=unsafe,id=drive-virtio-disk%d", img.path, i))
		qemuargs = append(qemuargs, "-device",
			fmt.Sprintf("virtio-blk-pci,drive=drive-virtio-disk%d,id=virtio-disk%d,serial=%s,logical_block_size=%d,physical_block_size=%d",
				i, i, img.label, m.sectorSize, m.sectorSize))
	}

	qemuargs = append(qemuargs, "-append", strings.Join(kernelargs, " "))

	pa := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	qemubin, err := b.QemuPath()
	if err != nil {
		return false, err
	}

	p, err := os.StartProcess(qemubin, qemuargs, &pa)
	if err != nil {
		return false, fmt.Errorf("failed to start qemu process: %w", err)
	}

	// wait for kvm process to exit
	pstate, err := p.Wait()
	if err != nil {
		return false, fmt.Errorf("error waiting for qemu process: %w", err)
	}

	return pstate.Success(), nil
}

type kvmBackend struct {
	qemuBackend
}

func newKvmBackend(m *Machine) kvmBackend {
	return kvmBackend{qemuBackend{machine: m}}
}

func (b kvmBackend) Name() string {
	return "kvm"
}

func (b kvmBackend) Supported() (bool, error) {
	kvmDevice, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("failed to open /dev/kvm: %w", err)
	}
	if err := kvmDevice.Close(); err != nil {
		return false, fmt.Errorf("failed to close /dev/kvm: %w", err)
	}

	return b.qemuBackend.Supported()
}

func (b kvmBackend) Start() (bool, error) {
	return b.StartQemu(true)
}

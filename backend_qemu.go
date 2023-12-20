//go:build linux && (arm64 || amd64)

package fakemachine

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
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
		/* The default cpu is a 32 bit one, which isn't very usefull
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

	return exec.LookPath(machine.binary)
}

func (b qemuBackend) KernelRelease() (string, error) {
	/* First try the kernel the current system is running, but if there are no
	 * modules for that try the latest from /lib/modules. The former works best
	 * for systems directly running fakemachine, the latter makes sense in docker
	 * environments */
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", err
	}
	release := string(u.Release[:bytes.IndexByte(u.Release[:], 0)])

	if _, err := os.Stat(path.Join("/lib/modules", release)); err == nil {
		return release, nil
	}

	files, err := ioutil.ReadDir("/lib/modules")
	if err != nil {
		return "", err
	}

	for i := len(files) - 1; i >= 0; i-- {
		/* Ensure the kernel name starts with a digit, in order
		 * to filter out 'extramodules-ARCH' on ArchLinux */
		filename := files[i].Name()
		if filename[0] >= '0' && filename[0] <= '9' {
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
	if moddir, err := b.ModulePath(); err == nil {
		kernelPath := path.Join(moddir, "vmlinuz")
		if _, err := os.Stat(kernelPath); err == nil {
			return kernelPath, nil
		}
	}

	/* Fall-back to the previous method and look in /boot */
	kernelRelease, err := b.KernelRelease()
	if err != nil {
		return "", err
	}

	kernelPath := "/boot/vmlinuz-" + kernelRelease
	if _, err := os.Stat(kernelPath); err != nil {
		return "", err
	}

	return kernelPath, nil
}

func (b qemuBackend) ModulePath() (string, error) {
	kernelRelease, err := b.KernelRelease()
	if err != nil {
		return "", err
	}

	moddir := "/lib/modules"
	if mergedUsrSystem() {
		moddir = "/usr/lib/modules"
	}

	moddir = path.Join(moddir, kernelRelease)
	if _, err := os.Stat(moddir); err != nil {
		return "", err
	}

	return moddir, nil
}

func (b qemuBackend) UdevRules() []string {
	udevRules := []string{}

	// create symlink under /dev/disk/by-fakemachine-label/ for each virtual image
	for i, img := range b.machine.images {
		driveLetter := 'a' + i
		udevRules = append(udevRules,
			fmt.Sprintf(`KERNEL=="vd%c", SYMLINK+="disk/by-fakemachine-label/%s"`, driveLetter, img.label),
			fmt.Sprintf(`KERNEL=="vd%c[0-9]", SYMLINK+="disk/by-fakemachine-label/%s-part%%n"`, driveLetter, img.label))
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

func (b qemuBackend) MountParameters(mount mountPoint) (string, []string) {
	return "9p", []string{"trans=virtio", "version=9p2000.L", "cache=loose", "msize=262144"}
}

func (b qemuBackend) InitModules() []string {
	return []string{"virtio_pci", "virtio_console", "9pnet_virtio", "9p"}
}

func (b qemuBackend) InitStaticVolumes() []mountPoint {
	return []mountPoint{}
}

func (b qemuBackend) Start() (bool, error) {
	return b.StartQemu(false)
}

func (b qemuBackend) StartQemu(kvm bool) (bool, error) {
	m := b.machine
	qemuMachine := qemuMachines[m.arch]

	kernelPath, err := b.KernelPath()
	if err != nil {
		return false, err
	}
	memory := fmt.Sprintf("%d", m.memory)
	numcpus := fmt.Sprintf("%d", m.numcpus)
	qemuargs := []string{qemuMachine.binary,
		"-smp", numcpus,
		"-m", memory,
		"-kernel", kernelPath,
		"-initrd", m.initrdpath,
		"-display", "none",
		"-nic", "user,model=virtio-net-pci",
		"-no-reboot"}

	if kvm {
		qemuargs = append(qemuargs,
			"-cpu", "host",
			"-enable-kvm")
	} else if qemuMachine.qemuCPU != "" {
		qemuargs = append(qemuargs, "-cpu", qemuMachine.qemuCPU)
	}

	qemuargs = append(qemuargs, "-machine", qemuMachine.machine)
	console := fmt.Sprintf("console=%s", qemuMachine.console)
	kernelargs := []string{console, "panic=-1",
		"plymouth.enable=0",
		"systemd.unit=fakemachine.service"}

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

	for _, point := range m.mounts {
		qemuargs = append(qemuargs, "-virtfs",
			fmt.Sprintf("local,mount_tag=%s,path=%s,security_model=none,multidevs=remap",
				point.label, point.hostDirectory))
	}

	for i, img := range m.images {
		qemuargs = append(qemuargs, "-drive",
			fmt.Sprintf("file=%s,if=none,format=raw,cache=unsafe,id=drive-virtio-disk%d", img.path, i))
		qemuargs = append(qemuargs, "-device",
			fmt.Sprintf("virtio-blk-pci,drive=drive-virtio-disk%d,id=virtio-disk%d,serial=%s",
				i, i, img.label))
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
		return false, err
	}

	// wait for kvm process to exit
	pstate, err := p.Wait()
	if err != nil {
		return false, err
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
		return false, err
	}
	kvmDevice.Close()

	return b.qemuBackend.Supported()
}

func (b kvmBackend) Start() (bool, error) {
	return b.StartQemu(true)
}

// +build linux
// +build amd64

package fakemachine

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type kvmBackend struct {
	machine *Machine
}

func newKvmBackend(m *Machine) kvmBackend {
	return kvmBackend{machine: m}
}

func (b kvmBackend) Name() string {
	return "kvm"
}

func (b kvmBackend) Supported() (bool, error) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return false, err
	}

	if _, err := b.QemuPath(); err != nil {
		return false, err
	}
	return true, nil
}

func (b kvmBackend) QemuPath() (string, error) {
	return exec.LookPath("qemu-system-x86_64")
}

func (b kvmBackend) Start() (bool, error) {
	m := b.machine

	kernelPath, _, err := hostKernelPath()
	if err != nil {
		return false, err
	}
	memory := fmt.Sprintf("%d", m.memory)
	numcpus := fmt.Sprintf("%d", m.numcpus)
	qemuargs := []string{"qemu-system-x86_64",
		"-cpu", "host",
		"-smp", numcpus,
		"-m", memory,
		"-enable-kvm",
		"-kernel", kernelPath,
		"-initrd", m.initrdpath,
		"-display", "none",
		"-no-reboot"}
	kernelargs := []string{"console=ttyS0", "panic=-1",
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
			fmt.Sprintf("local,mount_tag=%s,path=%s,security_model=none",
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

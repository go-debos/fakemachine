package fakemachine

import (
	"fmt"
	"log"
	"os"
	"io/ioutil"
	"path"
	"strings"
	"syscall"

	"fakemachine/cpio"
)

func mergedUsrSystem () bool {
  f, _ := os.Lstat("/bin")

  if (f.Mode() & os.ModeSymlink) == os.ModeSymlink {
    return true
  }

  return false
}


type mountPoint struct {
	hostDirectory string
	machineDirectory string
	label     string
}

type Machine struct {
	mounts  []mountPoint
	count   int
	Command string
}

func NewMachine() (m *Machine) {
	m = &Machine{Command: "/bin/bash"}
	// usr is mounted by specific label via /init
	m.AppendStaticVirtFS("/usr", "usr")

	if ! mergedUsrSystem() {
		m.AppendStaticVirtFS("/sbin", "sbin")
		m.AppendStaticVirtFS("/bin", "bin")
		m.AppendStaticVirtFS("/lib", "lib")
	}
	// Mount for ssl certificates
	if _, err := os.Stat("/etc/ssl"); err == nil {
		m.AppendVirtFS("/etc/ssl")
	}

	// Alternative symlinks
	m.AppendVirtFS("/etc/alternatives")

	return
}

func charsToString(in []int8) string {
	s := make([]byte, len(in))

	i := 0
	for ; i < len(in); i++ {
		if in[i] == 0 {
			break
		}
		s[i] = byte(in[i])
	}

	return string(s[0:i])
}

const InitrdPath = "/tmp/initramfs.go.cpio"

const InitScript = `#!/usr/bin/busybox sh

busybox mount -t proc proc /proc
busybox mount -t sysfs none /sys

busybox modprobe virtio_pci
busybox modprobe 9pnet_virtio
busybox modprobe 9p

busybox mount -v -t 9p -o trans=virtio,version=9p2000.L usr /usr
if ! busybox test -L /bin ; then
	busybox mount -v -t 9p -o trans=virtio,version=9p2000.L sbin /sbin
	busybox mount -v -t 9p -o trans=virtio,version=9p2000.L bin /bin
	busybox mount -v -t 9p -o trans=virtio,version=9p2000.L lib /lib
fi
exec /lib/systemd/systemd
`

const Networkd = `
[Match]
Name=e*

[Network]
DHCP=yes
`

const ServiceTemplate = `
[Unit]
Description=Qemu wrap run
Conflicts=shutdown.target
Before=shutdown.target
Requires=systemd-networkd-wait-online.service
After=systemd-networkd-wait-online.service

[Service]
Environment=HOME=/root
WorkingDirectory=-/scratch
ExecStartPre=/bin/echo Running: %[1]s
ExecStart=%[1]s
ExecStopPost=/usr/bin/sync
ExecStopPost=/usr/sbin/poweroff -f
OnFailure=poweroff.target
Type=idle
StandardInput=tty-force
StandardOutput=inherit
StandardError=inherit
KillMode=process
IgnoreSIGPIPE=no
SendSIGHUP=yes
`

func (m *Machine) AppendStaticVirtFS(directory, label string) {
	m.mounts = append(m.mounts, mountPoint{directory, directory, label})
}

func (m *Machine) AppendVirtFSMachineDir(hostDirectory, machineDirectory string) {
	label := fmt.Sprintf("virtfs-%d", m.count)
	m.mounts = append(m.mounts, mountPoint{hostDirectory, machineDirectory, label})
	m.count = m.count + 1
}

func (m *Machine) AppendVirtFS(directory string) {
	m.AppendVirtFSMachineDir(directory, directory)
}

func (m *Machine) generateFstab(w *writerhelper.WriterHelper) {
	fstab := []string{"# Generated fstab file by fakemachine"}
	for _, point := range m.mounts {
		fstab = append(fstab,
			fmt.Sprintf("%s %s 9p trans=virtio,version=9p2000.L 0 0",
				point.label, point.machineDirectory))
	}
	fstab = append(fstab, "")

	w.WriteFile("/etc/fstab", strings.Join(fstab, "\n"), 0755)
}

func (m *Machine) kernelRelease() string {
	/* First try the kernel the current system is running, but if there are no
	 * modules for that try the latest from /lib/modules. The former works best
	 * for systems direclty running fakemachine, the latter makes sense in docker
	 * environments */
	var u syscall.Utsname
	syscall.Uname(&u)
	release :=  charsToString(u.Release[:])

	if _, err := os.Stat(path.Join("/lib/modules", release)); err == nil {
		return release
	}

	files, err := ioutil.ReadDir("/usr/lib/modules")
	if err != nil {
		log.Fatal(err)
	}

	if len(files) == 0 {
		log.Fatal("No kernel found")
	}

	return (files[len(files) - 1]).Name()
}

func (m *Machine) writerKernelModules(w *writerhelper.WriterHelper) {
	kernelRelease := m.kernelRelease()

	modules := []string{
		"kernel/drivers/virtio/virtio.ko",
		"kernel/drivers/virtio/virtio_pci.ko",
		"kernel/net/9p/9pnet.ko",
		"kernel/drivers/virtio/virtio_ring.ko",
		"kernel/fs/9p/9p.ko",
		"kernel/net/9p/9pnet_virtio.ko",
		"kernel/fs/fscache/fscache.ko",
		"modules.order",
		"modules.builtin",
		"modules.dep",
		"modules.dep.bin",
		"modules.alias",
		"modules.alias.bin",
		"modules.softdep",
		"modules.symbols",
		"modules.symbols.bin",
		"modules.builtin.bin",
		"modules.devname"}

	for _, v := range modules {
		if mergedUsrSystem() {
			w.CopyFile(path.Join("/usr/lib/modules", kernelRelease, v))
		} else {
			w.CopyFile(path.Join("/lib/modules", kernelRelease, v))
		}
	}
}

func (m *Machine) Run() {
	f, err := os.OpenFile(InitrdPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)

	if err != nil {
		log.Fatal(err)
	}

	w := writerhelper.NewWriterHelper(f)

	w.WriteDirectory("/scratch", 01777)
	w.WriteDirectory("/var/tmp", 01777)
	w.WriteDirectory("/var/lib/dbus", 0755)

	w.WriteDirectory("/tmp", 01777)
	w.WriteDirectory("/sys", 0755)
	w.WriteDirectory("/proc", 0755)
	w.WriteDirectory("/run", 0755)
	w.WriteDirectory("/usr", 0755)
	w.WriteDirectory("/usr/bin", 0755)
	w.WriteDirectory("/lib64", 0755)

	w.WriteSymlink("/run", "/var/run", 0755)

	if mergedUsrSystem() {
		w.WriteSymlink("/usr/sbin", "/sbin", 0755)
		w.WriteSymlink("/usr/bin", "/bin", 0755)
		w.WriteSymlink("/usr/lib", "/lib", 0755)
	} else {
		w.WriteDirectory("/sbin", 0755)
		w.WriteDirectory("/bin", 0755)
		w.WriteDirectory("/lib", 0755)
	}

	prefix := ""
	if mergedUsrSystem() {
		prefix = "/usr"
	}
	w.CopyFile(prefix + "/lib/x86_64-linux-gnu/libc.so.6")
	w.CopyFile(prefix + "/bin/busybox")

	/* Amd64 dynamic linker */
	w.CopyFile("/lib64/ld-linux-x86-64.so.2")

	w.WriteCharDevice("/dev/console", 5, 1, 0700)

	// Linker configuration
	w.CopyFile("/etc/ld.so.conf")
	w.CopyTree("/etc/ld.so.conf.d")

	// Core system configuration
	w.WriteFile("/etc/machine-id", "", 0444)
	w.WriteFile("/etc/hostname", "fakemachine", 0444)

	w.CopyFile("/etc/passwd")
	w.CopyFile("/etc/group")
	w.CopyFile("/etc/nsswitch.conf")

	w.WriteFile("/etc/systemd/network/ethernet.network", Networkd, 0444)
	w.WriteSymlink(
		"/lib/systemd/resolv.conf",
		"/etc/resolv.conf",
		0755)
	w.WriteSymlink(
		"/lib/systemd/system/systemd-networkd.service",
		"/etc/systemd/system/multi-user.target.wants/systemd-networkd.service",
		0755)

	w.WriteSymlink(
		"/lib/systemd/system/systemd-resolved.service",
		"/etc/systemd/system/multi-user.target.wants/systemd-resolved.service",
		0755)

	w.WriteSymlink(
		"/lib/systemd/system/systemd-networkd.socket",
		"/etc/systemd/system/sockets.target.wants/systemd-networkd.socket",
		0755)

	m.writerKernelModules(w)

	w.WriteFile("etc/systemd/system/serial-getty@ttyS0.service",
		fmt.Sprintf(ServiceTemplate, m.Command), 0755)

	w.WriteFile("/init", InitScript, 0755)

	m.generateFstab(w)

	w.Close()
	f.Close()

	kernelRelease := m.kernelRelease()
	qemuargs := []string{"qemu-system-x86_64",
		"-cpu", "host",
		"-smp", "2",
		"-m", "2048",
		"-enable-kvm",
		"-kernel", "/boot/vmlinuz-" + kernelRelease,
		"-initrd", InitrdPath,
		"-nographic",
		"-no-reboot"}
	kernelargs := []string{"console=ttyS0", "quiet", "panic=-1"}

	for _, point := range m.mounts {
		qemuargs = append(qemuargs, "-virtfs",
			fmt.Sprintf("local,mount_tag=%s,path=%s,security_model=none",
				point.label, point.hostDirectory))
	}

	qemuargs = append(qemuargs, "-append", strings.Join(kernelargs, " "))

	pa := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	p, _ := os.StartProcess("/usr/bin/qemu-system-x86_64", qemuargs, &pa)
	p.Wait()
}

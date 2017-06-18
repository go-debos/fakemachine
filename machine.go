package fakemachine

import (
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"syscall"

  "fakemachine/cpio"
)

type mountPoint struct {
		directory string
		label string
	}

type Machine struct {
	mounts []mountPoint
	count int
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

for cmd in $(cat /proc/cmdline) ; do
  case ${cmd} in
    wrap.mount=*)
      LM=${cmd#wrap.mount=}
      L=${LM%:*}
      M=$(systemd-escape -u -p ${LM#*:})
      mkdir -p ${M}
      mount -v -t 9p -o trans=virtio,version=9p2000.L ${L} ${M}
      ;;
  esac
done

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
	m.mounts = append(m.mounts, mountPoint{directory, label})
}

func (m *Machine) AppendVirtFS(directory string) {
	label := fmt.Sprintf("virtfs-%d", m.count)
	m.mounts = append(m.mounts, mountPoint{directory, label})
	m.count = m.count + 1
}

func (m *Machine) Run() {
	m.AppendStaticVirtFS("/usr", "usr")
	m.AppendVirtFS("/etc/ssl")
	m.AppendVirtFS("/etc/alternatives")

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
	w.WriteSymlink("/usr/sbin", "/sbin", 0755)
	w.WriteSymlink("/usr/bin", "/bin", 0755)
	w.WriteSymlink("/usr/lib", "/lib", 0755)

	w.CopyFile("/lib64/ld-linux-x86-64.so.2")
	w.CopyFile("/usr/lib/x86_64-linux-gnu/libc.so.6")
	// TODO broken with non-merged usr
	w.CopyFile("/usr/bin/busybox")

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
		"/us//lib/systemd/system/systemd-networkd.service",
		"/etc/systemd/system/multi-user.target.wants/systemd-networkd.service",
		0755)

	w.WriteSymlink(
		"/etc/systemd/system/multi-user.target.wants/systemd-resolved.service",
		"/us//lib/systemd/system/systemd-resolved.service",
		0755)

	w.WriteSymlink(
		"/etc/systemd/system/sockets.target.wants/systemd-networkd.socket",
		"/usr/lib/systemd/system/systemd-networkd.socket",
		0755)

	// TODO kernel modues
	var u syscall.Utsname
	syscall.Uname(&u)
	kernelRelease := charsToString(u.Release[:])

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
		w.CopyFile(path.Join("/usr/lib/modules", kernelRelease, v))
	}

	w.WriteFile("etc/systemd/system/serial-getty@ttyS0.service",
		fmt.Sprintf(ServiceTemplate, "/bin/bash"), 0755)

	w.WriteFile("/init", InitScript, 0755)

	fstab := []string{"# Generated fstab file by fakemachine"}
	for _,point := range m.mounts {
		fstab = append(fstab,
			fmt.Sprintf("%s %s 9p trans=virtio,version=9p2000.L 0 0",
				point.label, point.directory))
	}
	fstab = append(fstab, "")

	w.WriteFile("/etc/fstab", strings.Join(fstab, "\n"), 0755)

	w.Close()
	f.Close()


	qemuargs := []string{"qemu-system-x86_64",
		"-cpu", "host",
		"-smp", "2",
		"-m", "2048",
		"-enable-kvm",
		"-kernel", "/boot/vmlinuz-" + kernelRelease,
		"-initrd", InitrdPath,
		"-nographic",
		"-no-reboot" }
	kernelargs := []string{"console=ttyS0", "quiet", "panic=-1"}

	for _,point := range m.mounts {
		qemuargs = append(qemuargs, "-virtfs",
			fmt.Sprintf("local,mount_tag=%s,path=%s,security_model=none",
			point.label, point.directory))
	}

	qemuargs = append(qemuargs, "-append", strings.Join(kernelargs, " "))

	pa := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	p, _ := os.StartProcess("/usr/bin/qemu-system-x86_64", qemuargs, &pa)
	p.Wait()
}

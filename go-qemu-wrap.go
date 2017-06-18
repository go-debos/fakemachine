package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/surma/gocpio"
)

var count int

type WriterHelper struct {
	paths map[string]bool
	*cpio.Writer
}

func NewWriterHelper(f io.Writer) *WriterHelper {
	return &WriterHelper{
		paths:  map[string]bool{"/": true},
		Writer: cpio.NewWriter(f),
	}
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

func EnsureBaseDirectory(w *WriterHelper, directory string) {
	d := path.Clean(directory)

	if w.paths[d] {
		return
	}

	components := strings.Split(directory, "/")
	collector := "/"

	for _, c := range components {
		collector = path.Join(collector, c)
		if w.paths[collector] {
			continue
		}

		WriteDirectory(w, collector, 0755)
	}
}

func WriteDirectory(w *WriterHelper, directory string, perm os.FileMode) {
	EnsureBaseDirectory(w, path.Dir(directory))

	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_DIR
	hdr.Name = directory
	hdr.Mode = int64(perm)

	w.WriteHeader(hdr)

	w.paths[directory] = true
}

func WriteFile(w *WriterHelper, file, content string, perm os.FileMode) {
	EnsureBaseDirectory(w, path.Dir(file))

	hdr := new(cpio.Header)

	bytes := []byte(content)

	hdr.Type = cpio.TYPE_REG
	hdr.Name = file
	hdr.Mode = int64(perm)
	hdr.Size = int64(len(bytes))

	w.WriteHeader(hdr)
	w.Write(bytes)
}

func WriteSymlink(w *WriterHelper, target, link string, perm os.FileMode) {
	EnsureBaseDirectory(w, path.Dir(link))
	hdr := new(cpio.Header)

	content := []byte(target)

	hdr.Type = cpio.TYPE_SYMLINK
	hdr.Name = link
	hdr.Mode = int64(perm)
	hdr.Size = int64(len(content))

	w.WriteHeader(hdr)
	w.Write(content)
}

func WriteCharDevice(w *WriterHelper, device string, major, minor int64,
	perm os.FileMode) {
	EnsureBaseDirectory(w, path.Dir(device))
	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_CHAR
	hdr.Name = device
	hdr.Mode = int64(perm)
	hdr.Devmajor = major
	hdr.Devminor = minor

	w.WriteHeader(hdr)
}

func CopyTree(w *WriterHelper, path string) {
	walker := func(p string, info os.FileInfo, err error) error {
		if info.Mode().IsDir() {
			WriteDirectory(w, p, info.Mode() & ^os.ModeType)
		} else if info.Mode().IsRegular() {
			CopyFile(w, p)
		} else {
			panic("No handled")
		}

		return nil
	}

	filepath.Walk(path, walker)
}

func CopyFile(w *WriterHelper, in string) error {
	EnsureBaseDirectory(w, path.Dir(in))

	f, err := os.Open(in)
	if err != nil {
		log.Panicf("open failed: %s - %v", in, err)
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_REG
	hdr.Name = in
	hdr.Mode = int64(info.Mode() & ^os.ModeType)
	hdr.Size = info.Size()

	w.WriteHeader(hdr)
	io.Copy(w, f)

	return nil
}

func AppendStaticVirtFS(qemuargs []string, directory, label string) []string {
	return append(qemuargs, "-virtfs",
		fmt.Sprintf("local,mount_tag=%s,path=%s,security_model=none", label, directory))
}

func AppendVirtFS(qemuargs, kernelargs []string, directory string) ([]string, []string) {

	label := fmt.Sprintf("virtfs-%d", count)
	count = count + 1
	cmd := exec.Command("systemd-escape", "-p", directory)
	output, _ := cmd.Output()
	kernelargs = append(kernelargs,
		fmt.Sprintf("wrap.mount=%s:%s", label, output))
	return AppendStaticVirtFS(qemuargs, directory, label), kernelargs
}

func main() {
	flag.Parse()

	f, err := os.OpenFile(InitrdPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)

	if err != nil {
		log.Fatal(err)
	}

	w := NewWriterHelper(f)

	WriteDirectory(w, "/scratch", 01777)
	WriteDirectory(w, "/tmp", 01777)
	WriteDirectory(w, "/var", 0755)
	WriteDirectory(w, "/var/tmp", 01777)
	WriteDirectory(w, "/var/lib", 0755)
	WriteDirectory(w, "/var/lib/dbus", 0755)
	WriteDirectory(w, "/sys", 0755)
	WriteDirectory(w, "/proc", 0755)
	WriteDirectory(w, "/run", 0755)
	WriteDirectory(w, "/usr", 0755)
	WriteDirectory(w, "/usr/bin", 0755)
	WriteDirectory(w, "/lib64", 0755)

	WriteSymlink(w, "/run", "/var/run", 0755)
	WriteSymlink(w, "/usr/sbin", "/sbin", 0755)
	WriteSymlink(w, "/usr/bin", "/bin", 0755)
	WriteSymlink(w, "/usr/lib", "/lib", 0755)

	CopyFile(w, "/lib64/ld-linux-x86-64.so.2")
	CopyFile(w, "/usr/lib/x86_64-linux-gnu/libc.so.6")
	// TODO broken with non-merged usr
	CopyFile(w, "/usr/bin/busybox")

	WriteCharDevice(w, "/dev/console", 5, 1, 0700)

	// Linker configuration
	CopyFile(w, "/etc/ld.so.conf")
	CopyTree(w, "/etc/ld.so.conf.d")

	// Core system configuration
	WriteFile(w, "/etc/machine-id", "", 0444)
	WriteFile(w, "/etc/hostname", "wrapped-box", 0444)

	CopyFile(w, "/etc/passwd")
	CopyFile(w, "/etc/group")
	CopyFile(w, "/etc/nsswitch.conf")

	WriteFile(w, "/etc/systemd/network/ethernet.network", Networkd, 0444)
	WriteSymlink(w,
		"/lib/systemd/resolv.conf",
		"/etc/resolv.conf",
		0755)
	WriteSymlink(w,
		"/us//lib/systemd/system/systemd-networkd.service",
		"/etc/systemd/system/multi-user.target.wants/systemd-networkd.service",
		0755)

	WriteSymlink(w,
		"/etc/systemd/system/multi-user.target.wants/systemd-resolved.service",
		"/us//lib/systemd/system/systemd-resolved.service",
		0755)

	WriteSymlink(w,
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
		CopyFile(w, path.Join("/usr/lib/modules", kernelRelease, v))
	}

	WriteFile(w, "etc/systemd/system/serial-getty@ttyS0.service",
		fmt.Sprintf(ServiceTemplate, "/bin/bash"), 0755)

	WriteFile(w, "/init", InitScript, 0755)

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
		"-no-reboot"}
	kernelargs := []string{"console=ttyS0", "quiet", "panic=-1"}

	qemuargs = AppendStaticVirtFS(qemuargs, "/usr", "usr")

	qemuargs, kernelargs = AppendVirtFS(qemuargs, kernelargs, "/etc/ssl")
	qemuargs, kernelargs = AppendVirtFS(qemuargs, kernelargs, "/etc/alternatives")

	qemuargs = append(qemuargs, "-append", strings.Join(kernelargs, " "))

	pa := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	p, _ := os.StartProcess("/usr/bin/qemu-system-x86_64", qemuargs, &pa)
	p.Wait()
}

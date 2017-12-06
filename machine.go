package fakemachine

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"syscall"

	"github.com/go-debos/fakemachine/cpio"
)

func mergedUsrSystem() bool {
	f, _ := os.Lstat("/bin")

	if (f.Mode() & os.ModeSymlink) == os.ModeSymlink {
		return true
	}

	return false
}

type mountPoint struct {
	hostDirectory    string
	machineDirectory string
	label            string
}

type image struct {
	path  string
	label string
}

type Machine struct {
	mounts []mountPoint
	count  int
	images []image
	memory int

	scratchsize int64
	scratchpath string
	scratchfile string
	scratchdev  string
}

// Create a new machine object
func NewMachine() (m *Machine) {
	m = &Machine{memory: 2048}
	// usr is mounted by specific label via /init
	m.addStaticVolume("/usr", "usr")

	if !mergedUsrSystem() {
		m.addStaticVolume("/sbin", "sbin")
		m.addStaticVolume("/bin", "bin")
		m.addStaticVolume("/lib", "lib")
	}
	// Mount for ssl certificates
	if _, err := os.Stat("/etc/ssl"); err == nil {
		m.AddVolume("/etc/ssl")
	}

	// Dbus configuration
	m.AddVolume("/etc/dbus-1")
	// Alternative symlinks
	m.AddVolume("/etc/alternatives")
	// Debians binfmt registry
	if _, err := os.Stat("/var/lib/binfmts"); err == nil {
		m.AddVolume("/var/lib/binfmts")
	}

	return
}

func InMachine() (ret bool) {
	_, ret = os.LookupEnv("IN_FAKE_MACHINE")

	return
}

func Supported() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
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

const initScript = `#!/bin/busybox sh

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

const networkd = `
[Match]
Name=e*

[Network]
DHCP=ipv4
# Disable link-local address to speedup boot
LinkLocalAddressing=no
IPv6AcceptRA=no
`
const commandWrapper = `#!/bin/sh

echo Running '%[1]s'
%[1]s
echo $? > /run/fakemachine/result
`

const serviceTemplate = `
[Unit]
Description=fakemachine runner
Conflicts=shutdown.target
Before=shutdown.target
Requires=systemd-networkd-wait-online.service
After=systemd-networkd-wait-online.service

[Service]
Environment=HOME=/root IN_FAKE_MACHINE=yes
WorkingDirectory=-/scratch
ExecStart=/wrapper
ExecStopPost=/bin/sync
ExecStopPost=/bin/systemctl poweroff -ff
OnFailure=poweroff.target
Type=idle
StandardInput=tty-force
StandardOutput=inherit
StandardError=inherit
KillMode=process
IgnoreSIGPIPE=no
SendSIGHUP=yes
`

func (m *Machine) addStaticVolume(directory, label string) {
	m.mounts = append(m.mounts, mountPoint{directory, directory, label})
}

// AddVolumeAt mounts hostDirectory from the host at machineDirectory in the
// fake machine
func (m *Machine) AddVolumeAt(hostDirectory, machineDirectory string) {
	label := fmt.Sprintf("virtfs-%d", m.count)
	m.mounts = append(m.mounts, mountPoint{hostDirectory, machineDirectory, label})
	m.count = m.count + 1
}

// AddVolume mounts directory from the host at the same location in the
// fake machine
func (m *Machine) AddVolume(directory string) {
	m.AddVolumeAt(directory, directory)
}

// CreateImageWithLabel creates an image file at path a given size and exposes
// it in the fake machine using the given label as the serial id. If size is -1
// then the image should already exist and the size isn't modified.
//
// label needs to be less then 20 characters due to limitations from qemu
//
// The returned string is the device path of the new image as seen inside
// fakemachine.
func (m *Machine) CreateImageWithLabel(path string, size int64, label string) (string,
	error) {
	if size < 0 {
		_, err := os.Stat(path)
		if err != nil {
			return "", err
		}
	}

	if len(label) >= 20 {
		return "", fmt.Errorf("Label '%s' too long; cannot be more then 20 characters", label)
	}

	for _, image := range m.images {
		if image.label == label {
			return "", fmt.Errorf("Label '%s' already exists", label)
		}
	}

	i, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0666)
	if err != nil {
		return "", err
	}

	if size >= 0 {
		err = i.Truncate(size)
		if err != nil {
			return "", err
		}
	}

	i.Close()
	m.images = append(m.images, image{path, label})

	return fmt.Sprintf("/dev/disk/by-id/virtio-%s", label), nil
}

// CreateImage does the same as CreateImageWithLabel but lets the library pick
// the label.
func (m *Machine) CreateImage(imagepath string, size int64) (string, error) {
	label := fmt.Sprintf("fakedisk-%d", len(m.images))

	return m.CreateImageWithLabel(imagepath, size, label)
}

// SetMemory sets the fakemachines amount of memory (in megabytes). Defaults to
// 2048 MB
func (m *Machine) SetMemory(memory int) {
	m.memory = memory
}

// SetScratch sets the size and location of on-disk scratch space to allocate
// (sparsely) for /scratch. If not set /scratch will be backed by memory. If
// Path is "" then the working directory is used as a default storage location
func (m *Machine) SetScratch(scratchsize int64, path string) {
	m.scratchsize = scratchsize
	if path == "" {
		m.scratchpath, _ = os.Getwd()
	} else {
		m.scratchpath = path
	}
}

func (m *Machine) generateFstab(w *writerhelper.WriterHelper) {
	fstab := []string{"# Generated fstab file by fakemachine"}

	if m.scratchfile == "" {
		fstab = append(fstab, "none /scratch tmpfs size=95% 0 0")
	} else {
		fstab = append(fstab, fmt.Sprintf("%s /scratch ext4 defaults,relatime 0 0",
			m.scratchdev))
	}

	for _, point := range m.mounts {
		fstab = append(fstab,
			fmt.Sprintf("%s %s 9p trans=virtio,version=9p2000.L 0 0",
				point.label, point.machineDirectory))
	}
	fstab = append(fstab, "")

	w.WriteFile("/etc/fstab", strings.Join(fstab, "\n"), 0755)
}

func (m *Machine) kernelRelease() (string, error) {
	/* First try the kernel the current system is running, but if there are no
	 * modules for that try the latest from /lib/modules. The former works best
	 * for systems direclty running fakemachine, the latter makes sense in docker
	 * environments */
	var u syscall.Utsname
	syscall.Uname(&u)
	release := charsToString(u.Release[:])

	if _, err := os.Stat(path.Join("/lib/modules", release)); err == nil {
		return release, nil
	}

	files, err := ioutil.ReadDir("/lib/modules")
	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		return "", fmt.Errorf("No kernel found")
	}

	return (files[len(files)-1]).Name(), nil
}

func (m *Machine) writerKernelModules(w *writerhelper.WriterHelper) error {
	kernelRelease, err := m.kernelRelease()
	if err != nil {
		return err
	}

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
		usrpath := "/lib/modules"
		if mergedUsrSystem() {
			usrpath = "/usr/lib/modules"
		}
		if err := w.CopyFile(path.Join(usrpath, kernelRelease, v)); err != nil {
			return err
		}
	}
	return nil
}

func (m *Machine) setupscratch() error {
	if m.scratchsize == 0 {
		return nil
	}

	tmpfile, err := ioutil.TempFile(m.scratchpath, "fake-scratch.img.")
	if err != nil {
		return err
	}
	m.scratchfile = tmpfile.Name()

	m.scratchdev, err = m.CreateImageWithLabel(tmpfile.Name(), m.scratchsize, "fake-scratch")
	if err != nil {
		return err
	}
	mkfs := exec.Command("mkfs.ext4", "-q", tmpfile.Name())
	err = mkfs.Run()

	return err
}

func (m *Machine) cleanup() {
	if m.scratchfile != "" {
		os.Remove(m.scratchfile)
	}

	m.scratchfile = ""
}

// Start the machine running the given command and adding the extra content to
// the cpio. Extracontent is a list of {source, dest} tuples
func (m *Machine) startup(command string, extracontent [][2]string) (int, error) {
	defer m.cleanup()

	tmpdir, err := ioutil.TempDir("", "fakemachine-")
	if err != nil {
		return -1, err
	}
	m.AddVolumeAt(tmpdir, "/run/fakemachine")
	defer os.RemoveAll(tmpdir)

	err = m.setupscratch()
	if err != nil {
		return -1, err
	}

	InitrdPath := path.Join(tmpdir, "initramfs.cpio")
	f, err := os.OpenFile(InitrdPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)

	if err != nil {
		return -1, err
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

	w.WriteFile("/etc/systemd/network/ethernet.network", networkd, 0444)
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

	w.WriteSymlink(
		"/lib/systemd/system/binfmt-support.service",
		"/etc/systemd/system/multi-user.target.wants/binfmt-support.service",
		0755)

	m.writerKernelModules(w)

	w.WriteFile("etc/systemd/system/serial-getty@ttyS0.service",
		serviceTemplate, 0755)

	w.WriteFile("/wrapper",
		fmt.Sprintf(commandWrapper, command), 0755)

	w.WriteFile("/init", initScript, 0755)

	m.generateFstab(w)

	for _, v := range extracontent {
		w.CopyFileTo(v[0], v[1])
	}

	w.Close()
	f.Close()

	kernelRelease, err := m.kernelRelease()
	if err != nil {
		return -1, err
	}
	memory := fmt.Sprintf("%d", m.memory)
	qemuargs := []string{"qemu-system-x86_64",
		"-cpu", "host",
		"-smp", "2",
		"-m", memory,
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

	if p, err := os.StartProcess("/usr/bin/qemu-system-x86_64", qemuargs, &pa); err != nil {
		return -1, err
	} else {
		p.Wait()
	}

	result, err := os.Open(path.Join(tmpdir, "result"))
	if err != nil {
		return -1, err
	}

	exitstr, _ := ioutil.ReadAll(result)
	exitcode, err := strconv.Atoi(strings.TrimSpace(string(exitstr)))

	if err != nil {
		return -1, err
	}

	return exitcode, nil
}

// Run creates the machine running the given command
func (m *Machine) Run(command string) (int, error) {
	return m.startup(command, nil)
}

// RunInMachineWithArgs runs the caller binary inside the fakemachine with the
// specified commandline arguments
func (m *Machine) RunInMachineWithArgs(args []string) (int, error) {
	name := path.Join("/", path.Base(os.Args[0]))

	// FIXME: shell escaping?
	command := strings.Join(append([]string{name}, args...), " ")

	executable, err := exec.LookPath(os.Args[0])

	if err != nil {
		return -1, fmt.Errorf("Failed to find executable: %v\n", err)
	}

	return m.startup(command, [][2]string{{executable, name}})
}

// RunInMachine runs the caller binary inside the fakemachine with the same
// commandline arguments as the parent
func (m *Machine) RunInMachine() (int, error) {
	name := path.Join("/", path.Base(os.Args[0]))

	// FIXME: shell escaping?
	command := strings.Join(append([]string{name}, os.Args[1:]...), " ")

	return m.startup(command, [][2]string{{os.Args[0], name}})
}

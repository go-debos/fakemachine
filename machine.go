// +build linux
// +build amd64

package fakemachine

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
	"text/template"

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
	static           bool
}

type image struct {
	path  string
	label string
}

type Machine struct {
	backend  backend
	mounts   []mountPoint
	count    int
	images   []image
	memory   int
	numcpus  int
	showBoot bool
	Environ  []string

	scratchsize int64
	scratchpath string
	scratchfile string
	scratchdev  string
	initrdpath  string
}

// Create a new machine object with the auto backend
func NewMachine() *Machine {
	m, err := NewMachineWithBackend("auto")
	if err != nil {
		panic(err)
	}
	return m
}

// Create a new machine object
func NewMachineWithBackend(backendName string) (*Machine, error) {
	var err error
	m := &Machine{memory: 2048, numcpus: runtime.NumCPU()}

	m.backend, err = newBackend(backendName, m)
	if err != nil {
		return nil, err
	}

	// usr is mounted by specific label via /init
	m.addStaticVolume("/usr", "usr")

	if !mergedUsrSystem() {
		m.addStaticVolume("/sbin", "sbin")
		m.addStaticVolume("/bin", "bin")
		m.addStaticVolume("/lib", "lib")
	}

	// Mounts for ssl certificates
	if _, err := os.Stat("/etc/ca-certificates"); err == nil {
		m.AddVolume("/etc/ca-certificates")
	}
	if _, err := os.Stat("/etc/ssl"); err == nil {
		m.AddVolume("/etc/ssl")
	}

	// Dbus configuration
	m.AddVolume("/etc/dbus-1")
	// Debian alternative symlinks
	if _, err := os.Stat("/etc/alternatives"); err == nil {
		m.AddVolume("/etc/alternatives")
	}
	// Debians binfmt registry
	if _, err := os.Stat("/var/lib/binfmts"); err == nil {
		m.AddVolume("/var/lib/binfmts")
	}

	return m, nil
}

func InMachine() (ret bool) {
	_, ret = os.LookupEnv("IN_FAKE_MACHINE")

	return
}

// Check whether the auto backend is supported
func Supported() bool {
	_, err := newBackend("auto", nil)
	return err == nil
}

const initScript = `#!/bin/busybox sh

busybox mount -t proc proc /proc
busybox mount -t sysfs none /sys

# probe additional modules
{{ range $m := .Backend.InitModules }}
busybox modprobe {{ $m }}
{{ end }}

# mount static volumes
{{ range $point := StaticVolumes .Machine }}
{{ MountVolume $.Backend $point }}
{{ end }}

exec /lib/systemd/systemd
`
const networkdTemplate = `
[Match]
Name=%[1]s

[Network]
DHCP=ipv4
# Disable link-local address to speedup boot
LinkLocalAddressing=no
IPv6AcceptRA=no
`
const commandWrapper = `#!/bin/sh
/lib/systemd/systemd-networkd-wait-online -q
if [ $? != 0 ]; then
  echo "WARNING: Network setup failed"
  echo "== Journal =="
  journalctl -a --no-pager
  echo "== networkd =="
  networkctl status
  networkctl list
  echo 1 > /run/fakemachine/result
  exit
fi

echo Running '%[2]s' using '%[1]s' backend
%[2]s
echo $? > /run/fakemachine/result
`

// The line 'Environment=%[2]s' is used for environment variables optionally
// configured using Machine.SetEnviron()
const serviceTemplate = `
[Unit]
Description=fakemachine runner
Conflicts=shutdown.target
Before=shutdown.target
Requires=basic.target
Wants=systemd-resolved.service binfmt-support.service systemd-networkd.service
After=basic.target systemd-resolved.service binfmt-support.service systemd-networkd.service
OnFailure=poweroff.target

[Service]
Environment=HOME=/root IN_FAKE_MACHINE=yes %[2]s
WorkingDirectory=-/scratch
ExecStart=/wrapper
ExecStopPost=/bin/sync
ExecStopPost=/bin/systemctl poweroff -ff
Type=idle
TTYPath=%[1]s
StandardInput=tty-force
StandardOutput=inherit
StandardError=inherit
KillMode=process
IgnoreSIGPIPE=no
SendSIGHUP=yes
LimitNOFILE=4096
`

// helper function to generate a mount command for a given mountpoint
func tmplMountVolume(b backend, m mountPoint) string {
	fsType, options := b.MountParameters(m)

	mntCommand := []string{"busybox", "mount", "-v"}
	mntCommand = append(mntCommand, "-t", fsType)
	if len(options) > 0 {
		mntCommand = append(mntCommand, "-o", strings.Join(options, ","))
	}
	mntCommand = append(mntCommand, m.label)
	mntCommand = append(mntCommand, m.machineDirectory)
	return strings.Join(mntCommand, " ")
}

// helper function to return the static volumes from a machine, since the mounts variable is unexported
// include the extra static mounts from the backend
func tmplStaticVolumes(m Machine) []mountPoint {
	mounts := []mountPoint{}
	for _, mount := range append(m.mounts, m.backend.InitStaticVolumes()...) {
		if mount.static {
			mounts = append(mounts, mount)
		}
	}
	return mounts
}

func executeInitScriptTemplate(m *Machine, b backend) []byte {
	helperFuncs := template.FuncMap{
		"MountVolume": tmplMountVolume,
		"StaticVolumes": tmplStaticVolumes,
	}

	type templateVars struct {
		Machine *Machine
		Backend backend
	}
	tmplVariables := templateVars{m, b}

	tmpl := template.Must(template.New("init").Funcs(helperFuncs).Parse(initScript))
	out := &bytes.Buffer{}
	if err := tmpl.Execute(out, tmplVariables); err != nil {
		panic(err)
	}
	return out.Bytes()
}

func (m *Machine) addStaticVolume(directory, label string) {
	m.mounts = append(m.mounts, mountPoint{directory, directory, label, true})
}

// AddVolumeAt mounts hostDirectory from the host at machineDirectory in the
// fake machine
func (m *Machine) AddVolumeAt(hostDirectory, machineDirectory string) {
	label := fmt.Sprintf("virtfs-%d", m.count)
	for _, mount := range m.mounts {
		if mount.hostDirectory == hostDirectory && mount.machineDirectory == machineDirectory {
			// Do not need to add already existing mount
			return
		}
	}
	m.mounts = append(m.mounts, mountPoint{hostDirectory, machineDirectory, label, false})
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

	return fmt.Sprintf("/dev/disk/by-fakemachine-label/%s", label), nil
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

// SetNumCPUs sets the number of CPUs exposed to the fakemachine. Defaults to
// the number of available cores in the system.
func (m *Machine) SetNumCPUs(numcpus int) {
	m.numcpus = numcpus
}

// SetShowBoot sets whether to show boot/console messages from the fakemachine.
func (m *Machine) SetShowBoot(showBoot bool) {
	m.showBoot = showBoot
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

func (m Machine) generateFstab(w *writerhelper.WriterHelper, backend backend) {
	fstab := []string{"# Generated fstab file by fakemachine"}

	if m.scratchfile == "" {
		fstab = append(fstab, "none /scratch tmpfs size=95% 0 0")
	} else {
		fstab = append(fstab, fmt.Sprintf("%s /scratch ext4 defaults,relatime 0 0",
			m.scratchdev))
	}

	for _, point := range m.mounts {
		fstype, options := backend.MountParameters(point)
		fstab = append(fstab,
			fmt.Sprintf("%s %s %s %s 0 0",
				point.label, point.machineDirectory, fstype, strings.Join(options, ",")))
	}
	fstab = append(fstab, "")

	w.WriteFile("/etc/fstab", strings.Join(fstab, "\n"), 0755)
}

func (m *Machine) SetEnviron(environ []string) {
	m.Environ = environ
}

func (m *Machine) writerKernelModules(w *writerhelper.WriterHelper, moddir string, modules []string) error {
	if len(modules) == 0 {
		return nil
	}

	modules = append(modules,
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
			"modules.devname")

	// build a list of built-in modules so that we don’t attempt to copy them
	var builtinModules = make(map[string]bool)

	f, err := os.Open(path.Join(moddir, "modules.builtin"))
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		module := scanner.Text()
		builtinModules[module] = true
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	for _, v := range modules {
		if builtinModules[v] {
			continue
		}

		modpath := path.Join(moddir, v)

		if strings.HasSuffix(modpath, ".ko") {
			if _, err := os.Stat(modpath); err != nil {
				modpath += ".xz"
			}
			if _, err := os.Stat(modpath); err != nil {
				return err
			}
		}

		if err := w.CopyFile(modpath); err != nil {
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

	os.Setenv("PATH", os.Getenv("PATH") + ":/sbin:/usr/sbin")

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

	m.initrdpath = path.Join(tmpdir, "initramfs.cpio")
	f, err := os.OpenFile(m.initrdpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)

	if err != nil {
		return -1, err
	}

	backend := m.backend

	_, kernelModuleDir, err := backend.KernelPath()
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
	w.CopyFile(prefix + "/lib/x86_64-linux-gnu/libresolv.so.2")
	w.CopyFile(prefix + "/lib/x86_64-linux-gnu/libc.so.6")

	// search for busybox; in some distros it's located under /sbin
	busybox, err := exec.LookPath("busybox")
	if err != nil {
		return -1, err
	}
	w.CopyFileTo(busybox, prefix + "/bin/busybox")

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

	// udev rules
	udevRules := strings.Join(backend.UdevRules(), "\n") + "\n"
	w.WriteFile("/etc/udev/rules.d/61-fakemachine.rules", udevRules, 0444)

	w.WriteFile("/etc/systemd/network/ethernet.network",
		fmt.Sprintf(networkdTemplate, backend.NetworkdMatch()), 0444)
	w.WriteSymlink(
		"/lib/systemd/resolv.conf",
		"/etc/resolv.conf",
		0755)

	m.writerKernelModules(w, kernelModuleDir, backend.InitrdModules())

	w.WriteFile("etc/systemd/system/fakemachine.service",
		fmt.Sprintf(serviceTemplate, backend.JobOutputTTY(), strings.Join(m.Environ, " ")), 0644)

	w.WriteSymlink(
		"/lib/systemd/system/serial-getty@ttyS0.service",
		"/dev/null",
		0755)

	w.WriteFile("/wrapper",
		fmt.Sprintf(commandWrapper, backend.Name(), command), 0755)

	w.WriteFileRaw("/init", executeInitScriptTemplate(m, backend), 0755)

	m.generateFstab(w, backend)

	for _, v := range extracontent {
		w.CopyFileTo(v[0], v[1])
	}

	w.Close()
	f.Close()

	success, err := backend.Start()
	if !success || err != nil {
		return -1, fmt.Errorf("error starting %s backend: %v", backend.Name(), err)
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

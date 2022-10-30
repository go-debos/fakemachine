//go:build linux && amd64
// +build linux,amd64

package fakemachine

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"

	"github.com/go-debos/fakemachine/cpio"
)

func mergedUsrSystem() bool {
	f, _ := os.Lstat("/bin")

	return (f.Mode() & os.ModeSymlink) == os.ModeSymlink
}

// Parse modinfo output and return the value of module attributes
// There may be multiple row with same fieldname so []string
// is used to return all data.
func getModData(modname string, fieldname string, kernelRelease string) []string {
	out, err := exec.Command("modinfo", "-k", kernelRelease, modname).Output()
	if err != nil {
		return nil
	}

	var fieldValue []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		field := strings.Split(strings.TrimSpace(scanner.Text()), ":")
		if strings.TrimSpace(field[0]) == fieldname {
			fieldValue = append(fieldValue, strings.TrimSpace(field[1]))
		}
	}
	return fieldValue
}

// Get full path of module
func getModPath(modname string, kernelRelease string) string {
	path := getModData(modname, "filename", kernelRelease)
	if len(path) != 0 {
		return path[0]
	}
	return ""
}

// Get all dependent module
func getModDepends(modname string, kernelRelease string) []string {
	deplist := getModData(modname, "depends", kernelRelease)
	var modlist []string
	for _, v := range deplist {
		if v != "" {
			modlist = append(modlist, strings.Split(v, ",")...)
		}
	}

	return modlist
}

var suffixes = map[string]writerhelper.Transformer{
	".ko":     NullDecompressor,
	".ko.gz":  GzipDecompressor,
	".ko.xz":  XzDecompressor,
	".ko.zst": ZstdDecompressor,
}

func (m *Machine) copyModules(w *writerhelper.WriterHelper, modname string, copiedModules map[string]bool) error {
	release, _ := m.backend.KernelRelease()
	modpath := getModPath(modname, release)
	if modpath == "" {
		return errors.New("Modules path couldn't be determined")
	}

	if modpath == "(builtin)" || copiedModules[modname] {
		return nil
	}

	prefix := ""
	if mergedUsrSystem() {
		prefix = "/usr"
	}

	found := false
	for suffix, fn := range suffixes {
		if strings.HasSuffix(modpath, suffix) {
			// File must exist as-is on the filesystem. Aka do not
			// fallback to other suffixes.
			if _, err := os.Stat(modpath); err != nil {
				return err
			}

			// The suffix is the complete thing - ".ko.foobar"
			// Reinstate the required ".ko" part, after trimming.
			basepath := strings.TrimSuffix(modpath, suffix) + ".ko"
			if err := w.TransformFileTo(modpath, prefix+basepath, fn); err != nil {
				return err
			}
			found = true
			break
		}
	}
	if !found {
		return errors.New("Module extension/suffix unknown")
	}

	copiedModules[modname] = true

	deplist := getModDepends(modname, release)
	for _, mod := range deplist {
		if err := m.copyModules(w, mod, copiedModules); err != nil {
			return err
		}
	}

	return nil
}

// Evaluate any symbolic link, then return the path's directory. Returns an
// absolute path. Think of it as realpath(1) + dirname(1) in bash.
func realDir(path string) (string, error) {
	var p string
	var err error
	if p, err = filepath.Abs(path); err != nil {
		return "", err
	}
	if p, err = filepath.EvalSymlinks(p); err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
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
	kernelpath  string
	initrdpath  string
}

// Create a new machine object with the auto backend
func NewMachine() (*Machine, error) {
	return NewMachineWithBackend("auto")
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
	if _, err := os.Stat("/etc/dbus-1"); err == nil {
		m.AddVolume("/etc/dbus-1")
	}

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

func executeInitScriptTemplate(m *Machine, b backend) ([]byte, error) {
	helperFuncs := template.FuncMap{
		"MountVolume":   tmplMountVolume,
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
		return nil, err
	}
	return out.Bytes(), nil
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

func (m *Machine) SetKernelPath(path string) {
	m.kernelpath = path
}

func (m Machine) generateFstab(w *writerhelper.WriterHelper, backend backend) error {
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

	err := w.WriteFile("/etc/fstab", strings.Join(fstab, "\n"), 0755)
	return err
}

func stripCompressionSuffix(module string) (string, error) {
	for suffix := range suffixes {
		if strings.HasSuffix(module, suffix) {
			// The suffix is the complete thing - ".ko.foobar"
			// Reinstate the required ".ko" part, after trimming.
			return strings.TrimSuffix(module, suffix) + ".ko", nil
		}
	}
	return "", errors.New("Module extension/suffix unknown")
}

func (m *Machine) generateModulesDep(w *writerhelper.WriterHelper, moddir string, modules map[string]bool) error {
	output := make([]string, len(modules))
	release, _ := m.backend.KernelRelease()
	i := 0
	for mod := range modules {
		modpath, _ := stripCompressionSuffix(getModPath(mod, release)) // CANNOT fail
		deplist := getModDepends(mod, release)                         // CANNOT fail
		deps := make([]string, len(deplist))
		for j, dep := range deplist {
			deppath, _ := stripCompressionSuffix(getModPath(dep, release)) // CANNOT fail
			deps[j] = deppath
		}
		output[i] = fmt.Sprintf("%s: %s", modpath, strings.Join(deps, " "))
		i += 1
	}

	path := path.Join(moddir, "modules.dep")
	return w.WriteFile(path, strings.Join(output, "\n"), 0644)
}

func (m *Machine) SetEnviron(environ []string) {
	m.Environ = environ
}

func (m *Machine) writerKernelModules(w *writerhelper.WriterHelper, moddir string, modules []string) error {
	if len(modules) == 0 {
		return nil
	}

	modfiles := []string{
		"modules.builtin",
		"modules.alias",
		"modules.symbols"}

	for _, v := range modfiles {
		if err := w.CopyFile(moddir + "/" + v); err != nil {
			return err
		}
	}

	copiedModules := make(map[string]bool)

	for _, modname := range modules {
		if err := m.copyModules(w, modname, copiedModules); err != nil {
			return err
		}
	}

	return m.generateModulesDep(w, moddir, copiedModules)
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

	os.Setenv("PATH", os.Getenv("PATH")+":/sbin:/usr/sbin")

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

	kernelModuleDir, err := backend.ModulePath()
	if err != nil {
		return -1, err
	}

	w := writerhelper.NewWriterHelper(f)

	err = w.WriteDirectories([]writerhelper.WriteDirectory{
		{Directory: "/scratch", Perm: 01777},
		{Directory: "/var/tmp", Perm: 01777},
		{Directory: "/var/lib/dbus", Perm: 0755},
		{Directory: "/tmp", Perm: 01777},
		{Directory: "/sys", Perm: 0755},
		{Directory: "/proc", Perm: 0755},
		{Directory: "/run", Perm: 0755},
		{Directory: "/usr", Perm: 0755},
		{Directory: "/usr/bin", Perm: 0755},
		{Directory: "/lib64", Perm: 0755},
	})
	if err != nil {
		return -1, err
	}

	err = w.WriteSymlink("/run", "/var/run", 0755)
	if err != nil {
		return -1, err
	}

	if mergedUsrSystem() {
		err = w.WriteSymlinks([]writerhelper.WriteSymlink{
			{Target: "/usr/sbin", Link: "/sbin", Perm: 0755},
			{Target: "/usr/bin", Link: "/bin", Perm: 0755},
			{Target: "/usr/lib", Link: "/lib", Perm: 0755},
		})
		if err != nil {
			return -1, err
		}
	} else {
		err = w.WriteDirectories([]writerhelper.WriteDirectory{
			{Directory: "/sbin", Perm: 0744},
			{Directory: "/bin", Perm: 0755},
			{Directory: "/lib", Perm: 0755},
		})
		if err != nil {
			return -1, err
		}
	}

	prefix := ""
	if mergedUsrSystem() {
		prefix = "/usr"
	}

	// search for busybox; in some distros it's located under /sbin
	busybox, err := exec.LookPath("busybox")
	if err != nil {
		return -1, err
	}
	err = w.CopyFileTo(busybox, prefix+"/bin/busybox")
	if err != nil {
		return -1, err
	}

	/* Ensure systemd-resolved is available */
	if _, err := os.Stat("/lib/systemd/systemd-resolved"); err != nil {
		return -1, err
	}

	/* Amd64 dynamic linker */
	err = w.CopyFile("/lib64/ld-linux-x86-64.so.2")
	if err != nil {
		return -1, err
	}

	/* C libraries */
	libraryDir, err := realDir("/lib64/ld-linux-x86-64.so.2")
	if err != nil {
		return -1, err
	}
	err = w.CopyFile(libraryDir + "/libc.so.6")
	if err != nil {
		return -1, err
	}
	err = w.CopyFile(libraryDir + "/libresolv.so.2")
	if err != nil {
		return -1, err
	}

	err = w.WriteCharDevice("/dev/console", 5, 1, 0700)
	if err != nil {
		return -1, err
	}

	// Linker configuration
	err = w.CopyFile("/etc/ld.so.conf")
	if err != nil {
		return -1, err
	}

	err = w.CopyTree("/etc/ld.so.conf.d")
	if err != nil {
		return -1, err
	}

	// Core system configuration
	err = w.WriteFile("/etc/machine-id", "", 0444)
	if err != nil {
		return -1, err
	}

	err = w.WriteFile("/etc/hostname", "fakemachine", 0444)
	if err != nil {
		return -1, err
	}

	err = w.CopyFile("/etc/passwd")
	if err != nil {
		return -1, err
	}

	err = w.CopyFile("/etc/group")
	if err != nil {
		return -1, err
	}

	err = w.CopyFile("/etc/nsswitch.conf")
	if err != nil {
		return -1, err
	}

	// udev rules
	udevRules := strings.Join(backend.UdevRules(), "\n") + "\n"
	err = w.WriteFile("/etc/udev/rules.d/61-fakemachine.rules", udevRules, 0444)
	if err != nil {
		return -1, err
	}

	err = w.WriteFile("/etc/systemd/network/ethernet.network",
		fmt.Sprintf(networkdTemplate, backend.NetworkdMatch()), 0444)
	if err != nil {
		return -1, err
	}

	err = w.WriteSymlink(
		"/lib/systemd/resolv.conf",
		"/etc/resolv.conf",
		0755)
	if err != nil {
		return -1, err
	}

	err = m.writerKernelModules(w, kernelModuleDir, backend.InitModules())
	if err != nil {
		return -1, err
	}

	err = w.WriteFile("etc/systemd/system/fakemachine.service",
		fmt.Sprintf(serviceTemplate, backend.JobOutputTTY(), strings.Join(m.Environ, " ")), 0644)
	if err != nil {
		return -1, err
	}

	err = w.WriteSymlink(
		"/lib/systemd/system/serial-getty@ttyS0.service",
		"/dev/null",
		0755)
	if err != nil {
		return -1, err
	}

	err = w.WriteFile("/wrapper",
		fmt.Sprintf(commandWrapper, backend.Name(), command), 0755)
	if err != nil {
		return -1, err
	}

	init, err := executeInitScriptTemplate(m, backend)
	if err != nil {
		return -1, err
	}

	err = w.WriteFileRaw("/init", init, 0755)
	if err != nil {
		return -1, err
	}

	err = m.generateFstab(w, backend)
	if err != nil {
		return -1, err
	}

	for _, v := range extracontent {
		err = w.CopyFileTo(v[0], v[1])
		if err != nil {
			return -1, err
		}
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

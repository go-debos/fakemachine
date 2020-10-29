// +build linux
// +build amd64

package fakemachine

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"

	"golang.org/x/sys/unix"
)

type umlBackend struct {
	machine *Machine
}

func newUmlBackend(m *Machine) umlBackend {
	return umlBackend{machine: m}
}

func (b umlBackend) Name() string {
	return "uml"
}

func (b umlBackend) Supported() (bool, error) {
	// check the kernel exists
	_, _, err := b.KernelPath()
	if err != nil {
		return false, err
	}

	// check the slirp helper exists exec.LookPath
	if _, err := b.SlirpHelperPath(); err != nil {
		return false, fmt.Errorf("libslirp-helper not installed")
	}
	return true, nil
}

func (b umlBackend) KernelPath() (string, string, error) {
	// find the UML binary
	kernelPath, err := exec.LookPath("linux.uml")
	if err != nil {
		return "", "", fmt.Errorf("user-mode-linux not installed")
	}

	// make sure the UML modules exist
	// on non-merged usr systems the modules still reside under /usr/lib/uml
	moddir := "/usr/lib/uml/modules"
	if _, err := os.Stat(moddir); err != nil {
		return "", "", fmt.Errorf("user-mode-linux modules not installed")
	}

	// find the subdirectory containing the modules for the UML release
	modSubdirs, err := ioutil.ReadDir(moddir)
	if err != nil {
		return "", "", err
	}
	if len(modSubdirs) != 1 {
		return "", "", fmt.Errorf("could not determine which user-mode-linux modules to use")
	}
	moddir = path.Join(moddir, modSubdirs[0].Name())

	return kernelPath, moddir, nil
}

func (b umlBackend) SlirpHelperPath() (string, error) {
	return exec.LookPath("libslirp-helper")
}

func (b umlBackend) InitrdModules() []string {
	return []string{}
}

func (b umlBackend) UdevRules() []string {
	udevRules := []string{}

	// create symlink under /dev/disk/by-fakemachine-label/ for each virtual image
	for i, img := range b.machine.images {
		driveLetter := 'a' + i
		udevRules = append(udevRules,
			fmt.Sprintf(`KERNEL=="ubd%c", SYMLINK+="disk/by-fakemachine-label/%s"`, driveLetter, img.label),
			fmt.Sprintf(`KERNEL=="ubd%c[0-9]", SYMLINK+="disk/by-fakemachine-label/%s-part%%n"`, driveLetter, img.label))
	}
	return udevRules
}

func (b umlBackend) NetworkdMatch() string {
	return "vec*"
}

func (b umlBackend) JobOutputTTY() string {
	// Send the fakemachine job output to the right console
	if b.machine.showBoot {
		return "/dev/tty0"
	}
	return "/dev/tty1"
}

func (b umlBackend) MountParameters(mount mountPoint) (fstype string, options []string) {
	fstype = "hostfs"
	options = []string{mount.hostDirectory}
	return
}

func (b umlBackend) InitModules() []string {
	return []string{}
}

func (b umlBackend) InitStaticVolumes() []mountPoint {
	// mount the UML modules over the top of /lib/modules
	// which currently contains the modules from the base system
	_, moddir, _ := b.KernelPath()
	moddir = path.Join(moddir, "../")

	machineDir := "/lib/modules"
	if mergedUsrSystem() {
		machineDir = "/usr/lib/modules"
	}

	moduleVolume := mountPoint{moddir, machineDir, "modules", true}
	return []mountPoint{moduleVolume}
}

func (b umlBackend) Start() (bool, error) {
	m := b.machine

	kernelPath, _, err := b.KernelPath()
	if err != nil {
		return false, err
	}

	slirpHelperPath, err := b.SlirpHelperPath()
	if err != nil {
		return false, err
	}

	/* for networking we use the UML vector transport alongside the
	 * libslirp-helper on the host. This works by creating a pair of
	 * connected sockets on the host using the socketpair syscall, which
	 * returns two file descriptors. One of the sockets is attached to the
	 * UML process and the other socket is attached to the libslirp-helper
	 * process allowing communication between the two processes.
	 * It doesn't matter the order in which the sockets are connected to
	 * the processes.
	 */
	netSocketpair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return false, err
	}

	// one of the sockets will be attached to the slirp-helper
	slirpHelperSocket := os.NewFile(uintptr(netSocketpair[0]), "")
	if slirpHelperSocket == nil {
		return false, fmt.Errorf("Creation of slirpHelperSocket failed")
	}
	defer slirpHelperSocket.Close()

	// while the other socket will be attached to the uml guest
	umlVectorTransportSocket := os.NewFile(uintptr(netSocketpair[1]), "")
	if umlVectorTransportSocket == nil {
		return false, fmt.Errorf("creation of umlVectorTransportSocket failed")
	}
	defer umlVectorTransportSocket.Close()


	// launch libslirp-helper
	slirpHelperArgs := []string{"libslirp-helper",
				    "--exit-with-parent"}

	/* attach the slirpHelperSocket as an additional fd to the process,
	 * after std*. The helper then bridges the host network to the attached
	 * file descriptor using the --fd argument. Since the standard std*
	 * file descriptors are passed to the libslirp-helper --fd should
	 * always be set to 3.
	 */
	slirpHelperAttr := &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr, slirpHelperSocket},
	}
	slirpHelperArgs = append(slirpHelperArgs, "--fd=3")

	slirpHelper, err := os.StartProcess(slirpHelperPath, slirpHelperArgs, slirpHelperAttr)
	if err != nil {
		return false, err
	}
	defer slirpHelper.Kill()


	// launch uml guest
	memory := fmt.Sprintf("%d", m.memory)
	umlargs := []string{"linux",
		"mem=" + memory + "M",
		"initrd=" + m.initrdpath,
		"panic=-1",
		"nosplash",
		"systemd.unit=fakemachine.service",
		"console=tty0",
	}

	/* umlVectorTransportSocket is attached as an additional fd to the process,
	 * after the std* file descriptors. Setup a vector device inside the guest
	 * which uses fd transport with the 3rd file descriptor attached to the
	 * UML process.
	 */
	umlAttr := &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr, umlVectorTransportSocket},
	}
	umlargs = append(umlargs, "vec0:transport=fd,fd=3,vec=0")

	if m.showBoot {
		// Create a character device representing our stdio
		// file descriptors, and connect the emulated serial
		// port (which is the console device for the BIOS,
		// Linux and systemd, and is also connected to the
		// fakemachine script) to that device
		umlargs = append(umlargs,
			"con0=fd:0,fd:1", // tty0 to stdin/stdout when showing boot
			"con=none")       // no other consoles
	} else {
		// don't show the UML message output by default
		umlargs = append(umlargs, "quiet")
		umlargs = append(umlargs,
			"con1=fd:0,fd:1",
			"con0=null",
			"con=none")       // no other consoles
	}

	for i, img := range m.images {
		umlargs = append(umlargs,
			fmt.Sprintf("ubd%d=%s", i, img.path))
	}

	p, err := os.StartProcess(kernelPath, umlargs, umlAttr)
	if err != nil {
		return false, err
	}

	// wait for uml process to exit
	ustate, err := p.Wait()
	if err != nil {
		return false, err
	}

	return ustate.Success(), nil
}

// +build linux
// +build amd64

package fakemachine

import(
	"fmt"
)

// A list of backends which are implemented
func BackendNames() []string {
	return []string{"auto", "kvm", "uml", "qemu"}
}

func newBackend(name string, m *Machine) (backend, error) {
	var b backend

	switch name {
	case "auto":
		// select kvm first
		kvm, kvm_err := newBackend("kvm", m)
		if kvm_err == nil {
			return kvm, nil
		}

		// falling back to uml
		uml, uml_err := newBackend("uml", m)
		if uml_err == nil {
			return uml, nil
		}

		// falling back to pure emulated qemu as fallback
		qemu, qemu_err := newBackend("qemu", m)
		if qemu_err == nil {
			return qemu, nil
		}

		// no backend supported
		return nil, fmt.Errorf("%v, %v, %v", kvm_err, uml_err, qemu_err)
	case "kvm":
		b = newKvmBackend(m)
	case "uml":
		b = newUmlBackend(m)
	case "qemu":
		b = newQemuBackend(m)
	default:
		return nil, fmt.Errorf("%s backend does not exist", name)
	}

	// check backend is supported
	if supported, err := b.Supported(); !supported {
		return nil, fmt.Errorf("%s backend not supported: %v", name, err)
	}

	return b, nil
}

type backend interface {
	// The name of the backend
	Name() string

	// Whether the backend is supported on this machine; if the backend is
	// not supported then the error contains a user-facing reason
	Supported() (bool, error)

	// Get kernel release version
	KernelRelease() (string, error)

	// The path to the kernel
	KernelPath() (kernelPath string, err error)

	// The path to the modules
	ModulePath() (moddir string, err error)

	// A list of udev rules
	UdevRules() []string

	// The match expression used for the networkd configuration
	NetworkdMatch() string

	// The tty used for the job output
	JobOutputTTY() string

	// The parameters used to mount a specific volume into the machine
	MountParameters(mount mountPoint) (fstype string, options []string)

	// A list of modules to be added to initrd and probed in the initscript
	InitModules() []string

	// A list of additional volumes which should mounted in the initscript
	InitStaticVolumes() []mountPoint

	// Start an instance of the backend
	Start() (bool, error)
}

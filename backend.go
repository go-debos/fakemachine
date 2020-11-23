// +build linux
// +build amd64

package fakemachine

import(
	"fmt"
)

// A list of backends which are implemented
func BackendNames() []string {
	return []string{"auto", "kvm"}
}

func newBackend(name string, m *Machine) (backend, error) {
	var b backend

	switch name {
	case "auto":
		fallthrough
	case "kvm":
		b = newKvmBackend(m)
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

	// The path to the kernel and modules
	KernelPath() (kernelPath string, moddir string, err error)

	// A list of modules to include in the initrd
	InitrdModules() []string

	// A list of udev rules
	UdevRules() []string

	// The match expression used for the networkd configuration
	NetworkdMatch() string

	// The tty used for the job output
	JobOutputTTY() string

	// The parameters used to mount a specific volume into the machine
	MountParameters(mount mountPoint) (fstype string, options []string)

	// A list of modules which should be probed in the initscript
	InitModules() []string

	// A list of additional volumes which should mounted in the initscript
	InitStaticVolumes() []mountPoint

	// Start an instance of the backend
	Start() (bool, error)
}

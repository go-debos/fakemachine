//go:build linux
// +build linux

package fakemachine

import (
	"fmt"
)

// List of backends in order of their priority in the "auto" algorithm
func implementedBackends(m *Machine) []backend {
	return []backend{
		newKvmBackend(m),
		newQemuBackend(m),
		newUmlBackend(m),
	}
}

/* A list of backends which are implemented - sorted in order in which the
 * "auto" backend chooses them.
 */
func BackendNames() []string {
	names := []string{"auto"}

	for _, backend := range implementedBackends(nil) {
		names = append(names, backend.Name())
	}

	return names
}

/* The "auto" backend loops through each backend, starting with the lowest order.
 * The backend is created and checked if the creation was successful (i.e. it is
 * supported on this machine). If so, that backend is used for the fakemachine. If
 * unsuccessful, the next backend is created until no more backends remain then
 * an error is thrown explaining why each backend was unsuccessful.
 */
func newBackend(name string, m *Machine) (backend, error) {
	backends := implementedBackends(m)
	var b backend
	var err error

	if name == "auto" {
		for _, backend := range backends {
			backendName := backend.Name()

			/* The user-mode-linux backend is flaky, don't allow users to auto-select it */
			if backendName == "uml" {
				continue
			}

			b, backendErr := newBackend(backendName, m)
			if backendErr != nil {
				/* Append the error to any existing backend creation error(s).
				 * Since we cannot join errors together in golang <1.20, instead
				 * join the error messages strings and return that as a new error.
				 */
				if err != nil {
					err = fmt.Errorf("%v, %v", err.Error(), backendErr.Error())
				} else {
					err = backendErr
				}
				continue
			}
			return b, nil
		}
		return nil, err
	}

	// find backend by name
	for _, backend := range backends {
		if backend.Name() == name {
			b = backend
		}
	}
	if b == nil {
		return nil, fmt.Errorf("%s backend does not exist", name)
	}

	// check backend is supported
	if supported, err := b.Supported(); !supported {
		return nil, fmt.Errorf("%s backend not supported: %w", name, err)
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

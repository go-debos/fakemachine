// +build linux
// +build amd64

package fakemachine

import (
	"os"
)

type kvmBackend struct {
	machine *Machine
}

func newKvmBackend(m *Machine) kvmBackend {
	return kvmBackend{machine: m}
}

func (b kvmBackend) Name() string {
	return "kvm"
}

func (b kvmBackend) Supported() (bool, error) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return false, err
	}
	return true, nil
}

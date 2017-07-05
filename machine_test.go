package fakemachine

import (
	"testing"
)

func TestSuccessfullCommand(t *testing.T) {
	m := NewMachine()
	m.Command = "ls /"

	exitcode := m.Run()

	if exitcode != 0 {
		t.Fatalf("Expected 0 but got %d", exitcode)
	}
}

func TestCommandNotFound(t *testing.T) {
	m := NewMachine()
	m.Command = "/a/b/c /"

	exitcode := m.Run()

	if exitcode != 127 {
		t.Fatalf("Expected 127 but got %d", exitcode)
	}
}

func TestImage(t *testing.T) {
	m := NewMachine()

	m.CreateImage("test.img", 1024*1024)
	m.Command = "test -b /dev/vda"

	exitcode := m.Run()

	if exitcode != 0 {
		t.Fatalf("Test for the virtual image device failed with %d", exitcode)
	}
}

func TestScratchTmp(t *testing.T) {
	m := NewMachine()

	m.Command = "mountpoint /scratch"

	exitcode := m.Run()

	if exitcode != 0 {
		t.Fatalf("Test for tmpfs mount on scratch failed with %d", exitcode)
	}
}

func TestMemory(t *testing.T) {
	m := NewMachine()

	m.SetMemory(1024)
// Nasty hack, this gets a chunk of shell script inserted in the wrapper script
// which is not really what fakemachine expects but seems good enough for
// testing
	m.Command = `
MEM=$(grep MemTotal /proc/meminfo  | awk ' { print $2 } ' )
# MemTotal is usable ram, not physical ram so accept a range
if [ ${MEM} -lt 900000 -o ${MEM} -gt 1024000 ] ; then
  exit 1
fi
`
	exitcode := m.Run()

	if exitcode != 0 {
		t.Fatalf("Test for tmpfs mount on scratch failed with %d", exitcode)
	}
}

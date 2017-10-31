package fakemachine

import (
	"bufio"
	"flag"
	"github.com/stretchr/testify/assert"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSuccessfullCommand(t *testing.T) {
	m := NewMachine()

	exitcode, _ := m.Run("ls /")

	if exitcode != 0 {
		t.Fatalf("Expected 0 but got %d", exitcode)
	}
}

func TestCommandNotFound(t *testing.T) {
	m := NewMachine()
	exitcode, _ := m.Run("/a/b/c /")

	if exitcode != 127 {
		t.Fatalf("Expected 127 but got %d", exitcode)
	}
}

func TestImage(t *testing.T) {
	m := NewMachine()

	m.CreateImage("test.img", 1024*1024)
	exitcode, _ := m.Run("test -b /dev/vda")

	if exitcode != 0 {
		t.Fatalf("Test for the virtual image device failed with %d", exitcode)
	}
}

func AssertMount(t *testing.T, mountpoint, fstype string) {
	m, err := os.Open("/proc/self/mounts")
	assert.Nil(t, err)

	mtab := bufio.NewReader(m)

	for {
		line, err := mtab.ReadString('\n')
		if err == io.EOF {
			assert.Fail(t, "mountpoint not found")
			break
		}
		assert.Nil(t, err)

		fields := strings.Fields(line)
		if fields[1] == mountpoint {
			assert.Equal(t, fields[2], fstype)
			return
		}
	}
}

func TestScratchTmp(t *testing.T) {
	if InMachine() {
		AssertMount(t, "/scratch", "tmpfs")
		return
	}

	m := NewMachine()

	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run TestScratchTmp"})

	if exitcode != 0 {
		t.Fatalf("Test for tmpfs mount on scratch failed with %d", exitcode)
	}
}

func TestScratchDisk(t *testing.T) {
	if InMachine() {
		AssertMount(t, "/scratch", "ext4")
		return
	}

	m := NewMachine()
	m.SetScratch(1024*1024*1024, "")

	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run TestScratchDisk"})

	if exitcode != 0 {
		t.Fatalf("Test for device mount on scratch failed with %d", exitcode)
	}
}

func TestMemory(t *testing.T) {
	m := NewMachine()

	m.SetMemory(1024)
	// Nasty hack, this gets a chunk of shell script inserted in the wrapper script
	// which is not really what fakemachine expects but seems good enough for
	// testing
	command := `
MEM=$(grep MemTotal /proc/meminfo  | awk ' { print $2 } ' )
# MemTotal is usable ram, not physical ram so accept a range
if [ ${MEM} -lt 900000 -o ${MEM} -gt 1024000 ] ; then
  exit 1
fi
`
	exitcode, _ := m.Run(command)

	if exitcode != 0 {
		t.Fatalf("Test for tmpfs mount on scratch failed with %d", exitcode)
	}
}

func TestSpawnMachine(t *testing.T) {

	if InMachine() {
		t.Log("Running in the machine")
		return
	}

	m := NewMachine()

	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run TestSpawnMachine"})

	if exitcode != 0 {
		t.Fatalf("Test for respawning in the machine failed failed with %d", exitcode)
	}
}

func TestImageLabel(t *testing.T) {
	if InMachine() {
		t.Log("Running in the machine")
		devices := flag.Args()
		assert.Equal(t, len(devices), 2, "Only expected two devices")

		autolabel := devices[0]
		labeled := devices[1]

		info, err := os.Stat(autolabel)
		assert.Nil(t, err)
		assert.Equal(t, info.Mode()&os.ModeType, os.ModeDevice, "Expected a device")

		info, err = os.Stat(labeled)
		assert.Nil(t, err)
		assert.Equal(t, info.Mode()&os.ModeType, os.ModeDevice, "Expected a device")

		return
	}

	m := NewMachine()
	autolabel, err := m.CreateImage("test-autolabel.img", 1024*1024)
	assert.Nil(t, err)

	labeled, err := m.CreateImageWithLabel("test-labeled.img", 1024*1024, "test-labeled")
	assert.Nil(t, err)

	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run TestImageLabel", autolabel, labeled})
	if exitcode != 0 {
		t.Fatalf("Test for images in the machine failed failed with %d", exitcode)
	}
}

package fakemachine

import (
	"bufio"
	"flag"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var backendName string
var testArg string

func init() {
	flag.StringVar(&backendName, "backend", "auto", "Fakemachine backend to use")
	flag.StringVar(&testArg, "testarg", "", "Test specific argument")
}

func CreateMachine(t *testing.T) *Machine {
	machine, err := NewMachineWithBackend(backendName)
	require.Nil(t, err)
	machine.SetNumCPUs(2)

	return machine
}

func TestSuccessfulCommand(t *testing.T) {
	t.Parallel()
	m := CreateMachine(t)

	exitcode, _ := m.Run("ls /")

	if exitcode != 0 {
		t.Fatalf("Expected 0 but got %d", exitcode)
	}
}

func TestCommandNotFound(t *testing.T) {
	t.Parallel()
	m := CreateMachine(t)
	exitcode, _ := m.Run("/a/b/c /")

	if exitcode != 127 {
		t.Fatalf("Expected 127 but got %d", exitcode)
	}
}

func TestImage(t *testing.T) {
	t.Parallel()
	m := CreateMachine(t)

	_, err := m.CreateImage("test.img", 1024*1024)
	require.Nil(t, err)
	exitcode, _ := m.Run("test -b /dev/disk/by-fakemachine-label/fakedisk-0")

	if exitcode != 0 {
		t.Fatalf("Test for the virtual image device failed with %d", exitcode)
	}
}

func AssertDevSectorSize(t *testing.T, device string, sectorsize int) {
	bstypes := []string{"physical", "logical"}
	for _, bstype := range bstypes {
		path := "/sys/block/" + device + "/queue/" + bstype + "_block_size"
		f, err := os.Open(path)
		require.Nil(t, err)
		defer f.Close()

		blkdev := bufio.NewReader(f)
		line, err := blkdev.ReadString('\n')
		require.Nil(t, err)

		line = strings.TrimSuffix(line, "\n")
		sz, err := strconv.Atoi(line)
		require.Nil(t, err)

		require.Equal(t, sectorsize, sz)
	}
}

func AssertSectorSize(t *testing.T, sectorsize int) {
	if !InMachine() {
		t.Parallel()
		m := CreateMachine(t)
		m.SetSectorSize(sectorsize)
		_, err := m.CreateImage("test-"+strconv.Itoa(sectorsize)+"-sector-size.img", 1024*1024)
		require.Nil(t, err)
		if sectorsize == 512 {
			exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run", "TestImage512SectorSize", backendName})
			require.Equal(t, exitcode, 0)
		} else if sectorsize == 4096 {
			exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run", "TestImage4kSectorSize", backendName})
			require.Equal(t, exitcode, 0)
		} else {
			t.Fatalf("Unhandled sector size %d", sectorsize)
		}
	} else {
		backend := flag.Args()
		if backend[0] == "uml" {
			AssertDevSectorSize(t, "ubda", sectorsize)
		} else {
			AssertDevSectorSize(t, "vda", sectorsize)
		}
	}
}

func TestImage512SectorSize(t *testing.T) {
	AssertSectorSize(t, 512)
}

func TestImage4kSectorSize(t *testing.T) {
	if backendName == "uml" {
		t.Skip("Skipping test for 4k sector size on uml backend (Not Implemented)")
	} else {
		AssertSectorSize(t, 4096)
	}
}

func AssertMount(t *testing.T, mountpoint, fstype string) {
	m, err := os.Open("/proc/self/mounts")
	require.Nil(t, err)

	mtab := bufio.NewReader(m)

	for {
		line, err := mtab.ReadString('\n')
		if err == io.EOF {
			require.Fail(t, "mountpoint not found")
			break
		}
		require.Nil(t, err)

		fields := strings.Fields(line)
		if fields[1] == mountpoint {
			require.Equal(t, fields[2], fstype)
			return
		}
	}
}

func TestScratchTmp(t *testing.T) {
	t.Parallel()
	if InMachine() {
		AssertMount(t, "/scratch", "tmpfs")
		return
	}

	m := CreateMachine(t)

	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run", "TestScratchTmp"})

	if exitcode != 0 {
		t.Fatalf("Test for tmpfs mount on scratch failed with %d", exitcode)
	}
}

func TestScratchDisk(t *testing.T) {
	t.Parallel()
	if InMachine() {
		AssertMount(t, "/scratch", "ext4")
		return
	}

	m := CreateMachine(t)
	m.SetScratch(1024*1024*1024, "")

	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run", "TestScratchDisk"})

	if exitcode != 0 {
		t.Fatalf("Test for device mount on scratch failed with %d", exitcode)
	}
}

func TestMemory(t *testing.T) {
	t.Parallel()
	m := CreateMachine(t)

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
		t.Fatalf("Test for set memory failed with %d", exitcode)
	}
}

func TestSpawnMachine(t *testing.T) {
	t.Parallel()
	if InMachine() {
		t.Log("Running in the machine")
		return
	}

	m := CreateMachine(t)

	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run", "TestSpawnMachine"})

	if exitcode != 0 {
		t.Fatalf("Test for respawning in the machine failed failed with %d", exitcode)
	}
}

func TestImageLabel(t *testing.T) {
	t.Parallel()
	if InMachine() {
		t.Log("Running in the machine")
		devices := flag.Args()
		require.Equal(t, len(devices), 2, "Only expected two devices")

		autolabel := devices[0]
		labeled := devices[1]

		info, err := os.Stat(autolabel)
		require.Nil(t, err)
		require.Equal(t, info.Mode()&os.ModeType, os.ModeDevice, "Expected a device")

		info, err = os.Stat(labeled)
		require.Nil(t, err)
		require.Equal(t, info.Mode()&os.ModeType, os.ModeDevice, "Expected a device")

		return
	}

	m := CreateMachine(t)
	autolabel, err := m.CreateImage("test-autolabel.img", 1024*1024)
	require.Nil(t, err)

	labeled, err := m.CreateImageWithLabel("test-labeled.img", 1024*1024, "test-labeled")
	require.Nil(t, err)

	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run", "TestImageLabel", autolabel, labeled})
	if exitcode != 0 {
		t.Fatalf("Test for images in the machine failed failed with %d", exitcode)
	}
}

func TestVolumes(t *testing.T) {
	t.Parallel()
	if InMachine() {
		t.Log("Running in the machine")
		return
	}

	/* Try to mount a non-existent file into the machine */
	m := CreateMachine(t)
	m.AddVolume("random_directory_never_exists")

	exitcode, err := m.RunInMachineWithArgs([]string{"-test.run", "TestVolumes"})
	require.Equal(t, exitcode, -1)
	require.Error(t, err)

	/* Try to mount a device file into the machine */
	m = CreateMachine(t)
	m.AddVolume("/dev/zero")

	exitcode, err = m.RunInMachineWithArgs([]string{"-test.run", "TestVolumes"})
	require.Equal(t, exitcode, -1)
	require.Error(t, err)

	/* Try to mount a volume with whitespace into the machine */
	m = CreateMachine(t)
	m.AddVolumeAt("/dev", "/dev ices")

	exitcode, err = m.RunInMachineWithArgs([]string{"-test.run", "TestVolumes"})
	require.Equal(t, exitcode, -1)
	require.Error(t, err)
}

func TestCommandEscaping(t *testing.T) {
	t.Parallel()
	if InMachine() {
		t.Log("Running in the machine")
		require.Equal(t, testArg, "$s'n\\akes")
		t.Log(testArg)
		return
	}

	m := CreateMachine(t)
	exitcode, _ := m.RunInMachineWithArgs([]string{
		"-test.v", "-test.run",
		"TestCommandEscaping", "-testarg", "$s'n\\akes"})

	if exitcode != 0 {
		t.Fatalf("Expected 0 but got %d", exitcode)
	}
}

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
	require.NoError(t, err)
	machine.SetNumCPUs(2)

	return machine
}

func TestSuccessfulCommand(t *testing.T) {
	t.Parallel()
	m := CreateMachine(t)

	exitcode, err := m.Run("ls /")
	require.NoError(t, err)
	require.Equal(t, 0, exitcode)
}

func TestCommandNotFound(t *testing.T) {
	t.Parallel()
	m := CreateMachine(t)

	exitcode, err := m.Run("/a/b/c /")
	require.NoError(t, err)
	require.Equal(t, 127, exitcode)
}

func TestImage(t *testing.T) {
	t.Parallel()
	m := CreateMachine(t)

	_, err := m.CreateImage("test.img", 1024*1024)
	require.NoError(t, err)
	exitcode, err := m.Run("test -b /dev/disk/by-fakemachine-label/fakedisk-0")
	require.NoError(t, err)
	require.Equal(t, 0, exitcode)
}

func AssertSectorSize(t *testing.T, sectorsize int) {
	t.Helper()
	t.Parallel()
	if InMachine() {
		for _, bstype := range []string{"physical", "logical"} {
			device := "vda"
			path := "/sys/block/" + device + "/queue/" + bstype + "_block_size"

			data, err := os.ReadFile(path)
			require.NoError(t, err)

			sz, err := strconv.Atoi(strings.TrimSpace(string(data)))
			require.NoError(t, err)

			require.Equal(t, sectorsize, sz)
		}
		return
	}

	m := CreateMachine(t)
	m.SetSectorSize(sectorsize)
	_, err := m.CreateImage("test-"+strconv.Itoa(sectorsize)+"-sector-size.img", 1024*1024)
	require.NoError(t, err)

	testName := ""
	switch sectorsize {
	case 512:
		testName = "TestImage512SectorSize"
	case 4096:
		testName = "TestImage4kSectorSize"
	default:
		t.Fatalf("Unhandled sector size %d", sectorsize)
	}

	exitcode, err := m.RunInMachineWithArgs([]string{"-test.run", testName, backendName})
	require.NoError(t, err)
	require.Equal(t, 0, exitcode)
}

func TestImage512SectorSize(t *testing.T) {
	AssertSectorSize(t, 512)
}

func TestImage4kSectorSize(t *testing.T) {
	AssertSectorSize(t, 4096)
}

func AssertMount(t *testing.T, mountpoint, fstype string) {
	m, err := os.Open("/proc/self/mounts")
	require.NoError(t, err)

	mtab := bufio.NewReader(m)

	for {
		line, err := mtab.ReadString('\n')
		if err == io.EOF {
			require.Fail(t, "mountpoint not found")
			break
		}
		require.NoError(t, err)

		fields := strings.Fields(line)
		if fields[1] == mountpoint {
			require.Equal(t, fstype, fields[2])
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

	exitcode, err := m.RunInMachineWithArgs([]string{"-test.run", "TestScratchTmp"})
	require.NoError(t, err)
	require.Equal(t, 0, exitcode)
}

func TestScratchDisk(t *testing.T) {
	t.Parallel()
	if InMachine() {
		AssertMount(t, "/scratch", "ext4")
		return
	}

	m := CreateMachine(t)
	m.SetScratch(1024*1024*1024, "")

	exitcode, err := m.RunInMachineWithArgs([]string{"-test.run", "TestScratchDisk"})
	require.NoError(t, err)
	require.Equal(t, 0, exitcode)
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
	exitcode, err := m.Run(command)
	require.NoError(t, err)
	require.Equal(t, 0, exitcode)
}

func TestSpawnMachine(t *testing.T) {
	t.Parallel()
	if InMachine() {
		t.Log("Running in the machine")
		return
	}

	m := CreateMachine(t)

	exitcode, err := m.RunInMachineWithArgs([]string{"-test.run", "TestSpawnMachine"})
	require.NoError(t, err)
	require.Equal(t, 0, exitcode)
}

func TestImageLabel(t *testing.T) {
	t.Parallel()
	if InMachine() {
		t.Log("Running in the machine")
		devices := flag.Args()
		require.Equal(t, 2, len(devices), "Only expected two devices")

		autolabel := devices[0]
		labeled := devices[1]

		info, err := os.Stat(autolabel)
		require.NoError(t, err)
		require.Equal(t, os.ModeDevice, info.Mode()&os.ModeType, "Expected a device")

		info, err = os.Stat(labeled)
		require.NoError(t, err)
		require.Equal(t, os.ModeDevice, info.Mode()&os.ModeType, "Expected a device")

		return
	}

	m := CreateMachine(t)
	autolabel, err := m.CreateImage("test-autolabel.img", 1024*1024)
	require.NoError(t, err)

	labeled, err := m.CreateImageWithLabel("test-labeled.img", 1024*1024, "test-labeled")
	require.NoError(t, err)

	exitcode, err := m.RunInMachineWithArgs([]string{"-test.run", "TestImageLabel", autolabel, labeled})
	require.NoError(t, err)
	require.Equal(t, 0, exitcode)
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
	require.Equal(t, -1, exitcode)
	require.Error(t, err)

	/* Try to mount a device file into the machine */
	m = CreateMachine(t)
	m.AddVolume("/dev/zero")

	exitcode, err = m.RunInMachineWithArgs([]string{"-test.run", "TestVolumes"})
	require.Equal(t, -1, exitcode)
	require.Error(t, err)

	/* Try to mount a volume with whitespace into the machine */
	m = CreateMachine(t)
	m.AddVolumeAt("/dev", "/dev ices")

	exitcode, err = m.RunInMachineWithArgs([]string{"-test.run", "TestVolumes"})
	require.Equal(t, -1, exitcode)
	require.Error(t, err)
}

func TestDiskSuffix(t *testing.T) {
	cases := []struct {
		i    int
		want string
	}{
		{0, "a"}, {1, "b"}, {25, "z"},
		{26, "aa"}, {27, "ab"}, {51, "az"},
		{52, "ba"}, {701, "zz"}, {702, "aaa"},
	}
	for _, c := range cases {
		if got := diskSuffix(c.i); got != c.want {
			t.Errorf("diskSuffix(%d) = %q, want %q", c.i, got, c.want)
		}
	}
}

func TestImageLabelUniqueness(t *testing.T) {
	m := CreateMachine(t)

	_, err := m.CreateImageWithLabel("test.img", 1024*1024, "my-disk")
	require.NoError(t, err)

	_, err = m.CreateImageWithLabel("test2.img", 1024*1024, "my-disk")
	require.Error(t, err)
}

func TestCommandEscaping(t *testing.T) {
	t.Parallel()
	if InMachine() {
		t.Log("Running in the machine")
		require.Equal(t, "$s'n\\akes", testArg)
		t.Log(testArg)
		return
	}

	m := CreateMachine(t)
	exitcode, err := m.RunInMachineWithArgs([]string{
		"-test.v", "-test.run",
		"TestCommandEscaping", "-testarg", "$s'n\\akes"})
	require.NoError(t, err)
	require.Equal(t, 0, exitcode)
}

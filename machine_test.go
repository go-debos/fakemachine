package fakemachine

import (
	"bufio"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type FakemachineTestSuite struct {
	suite.Suite

	backend string
}

// TODO create the machine automatically before the test using SetupTestSuite
func (suite *FakemachineTestSuite) CreateMachine() *Machine {
	machine, err := NewMachineWithBackend(suite.backend)
	if err != nil {
		suite.T().Error(err)
	}
	return machine
}

func (suite *FakemachineTestSuite) TestSuccessfulCommand() {
	m := suite.CreateMachine()

	exitcode, _ := m.Run("ls /")

	if exitcode != 0 {
		suite.T().Fatalf("Expected 0 but got %d", exitcode)
	}
}

func (suite *FakemachineTestSuite) TestCommandNotFound() {
	suite.T().Skip()
	m := suite.CreateMachine()
	exitcode, _ := m.Run("/a/b/c /")

	if exitcode != 127 {
		suite.T().Fatalf("Expected 127 but got %d", exitcode)
	}
}

func (suite *FakemachineTestSuite) TestImage() {
	m := suite.CreateMachine()

	m.CreateImage("test.img", 1024*1024)
	exitcode, _ := m.Run("test -b /dev/vda")

	if exitcode != 0 {
		suite.T().Fatalf("Test for the virtual image device failed with %d", exitcode)
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

func (suite *FakemachineTestSuite) TestScratchTmp() {
	if InMachine() {
		AssertMount(suite.T(), "/scratch", "tmpfs")
		return
	}

	m := suite.CreateMachine()

	// TODO error: "testing: warning: no tests to run"
	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run TestScratchTmp"})

	if exitcode != 0 {
		suite.T().Fatalf("Test for tmpfs mount on scratch failed with %d", exitcode)
	}
}

func (suite *FakemachineTestSuite) TestScratchDisk() {
	if InMachine() {
		AssertMount(suite.T(), "/scratch", "ext4")
		return
	}

	m := suite.CreateMachine()
	m.SetScratch(1024*1024*1024, "")

	// TODO error: "testing: warning: no tests to run"
	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run TestScratchDisk"})

	if exitcode != 0 {
		suite.T().Fatalf("Test for device mount on scratch failed with %d", exitcode)
	}
}

func (suite *FakemachineTestSuite) TestMemory() {
	m := suite.CreateMachine()

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
		suite.T().Fatalf("Test for tmpfs mount on scratch failed with %d", exitcode)
	}
}

func (suite *FakemachineTestSuite) TestSpawnMachine() {

	if InMachine() {
		suite.T().Log("Running in the machine")
		return
	}

	m := suite.CreateMachine()

	// TODO error: "testing: warning: no tests to run"
	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run TestSpawnMachine"})

	if exitcode != 0 {
		suite.T().Fatalf("Test for respawning in the machine failed failed with %d", exitcode)
	}
}

func (suite *FakemachineTestSuite) TestImageLabel() {
	if InMachine() {
		suite.T().Log("Running in the machine")
		devices := flag.Args()
		assert.Equal(suite.T(), len(devices), 2, "Only expected two devices")

		autolabel := devices[0]
		labeled := devices[1]

		info, err := os.Stat(autolabel)
		assert.Nil(suite.T(), err)
		assert.Equal(suite.T(), info.Mode()&os.ModeType, os.ModeDevice, "Expected a device")

		info, err = os.Stat(labeled)
		assert.Nil(suite.T(), err)
		assert.Equal(suite.T(), info.Mode()&os.ModeType, os.ModeDevice, "Expected a device")

		return
	}

	m := suite.CreateMachine()
	autolabel, err := m.CreateImage("test-autolabel.img", 1024*1024)
	assert.Nil(suite.T(), err)

	labeled, err := m.CreateImageWithLabel("test-labeled.img", 1024*1024, "test-labeled")
	assert.Nil(suite.T(), err)

	// TODO error: "testing: warning: no tests to run"
	exitcode, _ := m.RunInMachineWithArgs([]string{"-test.run TestImageLabel", autolabel, labeled})
	if exitcode != 0 {
		suite.T().Fatalf("Test for images in the machine failed failed with %d", exitcode)
	}
}

func TestFakemachineTestSuite(t *testing.T) {
	// TODO run some tests outside of fakemachine first - finding modules etc?
	// TODO test auto backend resolution
	// TODO try to speed up the test by bringing up just one fakemachine per backend?

	// test all backends
	suite.Run(t, &FakemachineTestSuite{backend: "auto"})
	suite.Run(t, &FakemachineTestSuite{backend: "kvm"})
	suite.Run(t, &FakemachineTestSuite{backend: "uml"})
}

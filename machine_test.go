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

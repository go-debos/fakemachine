//go:build linux && (arm64 || amd64)

package fakemachine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatCommandLine(t *testing.T) {
	args := []string{
		"qemu-system-x86_64",
		"-m", "2048",
		"-append", "console=ttyS0 panic=-1",
		"-virtfs", "local,path=/tmp/it's here",
		"",
	}

	require.Equal(t,
		`qemu-system-x86_64 -m 2048 -append 'console=ttyS0 panic=-1' `+
			`-virtfs 'local,path=/tmp/it'\''s here' ''`,
		formatCommandLine(args))
}

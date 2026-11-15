//go:build linux && (arm64 || amd64)

package fakemachine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestKernelPanic(t *testing.T) {
	cases := []struct {
		name    string
		console string
		want    string
	}{
		{
			name:    "no panic",
			console: "[    0.000000] Linux version 6.12\r\nWelcome to Debian\r\n",
			want:    "",
		},
		{
			name: "panic with timestamp",
			console: "[    0.000000] Linux version 6.12\r\n" +
				"[    1.234567] Kernel panic - not syncing: sysrq triggered crash\r\n" +
				"[    1.234568] CPU: 0 PID: 1 Comm: sh\r\n",
			want: "Kernel panic - not syncing: sysrq triggered crash",
		},
		{
			name:    "panic without timestamp or trailing newline",
			console: "Kernel panic - not syncing: Attempted to kill init!",
			want:    "Kernel panic - not syncing: Attempted to kill init!",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "console.log")
			//nolint:gosec // The test uses normal umask-controlled permissions.
			require.NoError(t, os.WriteFile(log, []byte(c.console), 0o644))

			msg, err := guestKernelPanic(log)
			require.NoError(t, err)
			require.Equal(t, c.want, msg)
		})
	}
}

func TestGuestKernelPanicMissingLog(t *testing.T) {
	msg, err := guestKernelPanic(filepath.Join(t.TempDir(), "missing.log"))
	require.NoError(t, err)
	require.Empty(t, msg)
}

func TestKernelPanic(t *testing.T) {
	m := CreateMachine(t)
	switch m.backend.Name() {
	case "qemu", "kvm":
	default:
		t.Skipf("panic detection is not supported by the %s backend", m.backend.Name())
	}

	// Writing c to sysrq-trigger crashes the kernel
	_, err := m.Run("echo c > /proc/sysrq-trigger")
	require.Error(t, err)
	require.ErrorContains(t, err, "guest kernel panic")
}

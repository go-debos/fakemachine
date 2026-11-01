//go:build linux && (arm64 || amd64)

package fakemachine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModulesBuiltin(t *testing.T) {
	builtin := "kernel/drivers/virtio/virtio_pci.ko\n" +
		"kernel/drivers/char/virtio_console.ko\n" +
		"kernel/net/9p/9pnet_virtio.ko\n"

	require.ErrorContains(t, checkModulesBuiltin("Image", "modules.builtin", []byte(builtin)),
		"modules 9p are not built")
	require.NoError(t, checkModulesBuiltin("Image", "modules.builtin", []byte(builtin+"kernel/fs/9p/9p.ko\n")))
}

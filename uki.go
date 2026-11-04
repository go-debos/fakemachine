//go:build linux && (arm64 || amd64)

package fakemachine

import (
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
)

// extractUKIKernel extracts the kernel image embedded in the .linux section
// of the image at kernelPath into dst if it is a unified kernel image, i.e. a
// systemd-stub EFI application, which qemu cannot boot directly, and returns
// dst. If the image is not a unified kernel image, kernelPath is returned.
func extractUKIKernel(kernelPath, dst string) (_ string, err error) {
	//nolint:gosec // kernelPath is the user supplied or host kernel image.
	f, err := os.Open(kernelPath)
	if err != nil {
		return "", fmt.Errorf("failed to open kernel %s: %w", kernelPath, err)
	}
	defer func() { _ = f.Close() }()

	pf, err := pe.NewFile(f)
	if err != nil {
		// Not a PE image, so not a unified kernel image either
		return kernelPath, nil
	}

	section := pf.Section(".linux")
	if section == nil {
		return kernelPath, nil
	}

	//nolint:gosec // dst is within the fakemachine temporary directory.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("failed to create %s: %w", dst, err)
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close %s: %w", dst, closeErr))
		}
	}()

	if _, err := io.Copy(out, section.Open()); err != nil {
		return "", fmt.Errorf("failed to extract kernel from unified kernel image %s: %w", kernelPath, err)
	}

	return dst, nil
}

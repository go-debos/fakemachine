//go:build linux && (arm64 || amd64)

package fakemachine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// zbootHeader is the start of the header of an EFI zboot image, as built by
// the kernel with CONFIG_EFI_ZBOOT, see
// drivers/firmware/efi/libstub/zboot-header.S in the kernel sources.
type zbootHeader struct {
	MZ            [2]byte
	_             [2]byte
	Type          [4]byte
	PayloadOffset uint32
	PayloadSize   uint32
	_             [8]byte
	Compression   [32]byte
}

// unpackZboot decompresses the kernel image at kernelPath into dst if it is
// an EFI zboot image, which qemu cannot boot directly, and returns dst. If
// the kernel image is not an EFI zboot image, kernelPath is returned.
func unpackZboot(kernelPath, dst string) (_ string, err error) {
	//nolint:gosec // kernelPath is the user supplied or host kernel image.
	f, err := os.Open(kernelPath)
	if err != nil {
		return "", fmt.Errorf("failed to open kernel %s: %w", kernelPath, err)
	}
	defer func() { _ = f.Close() }()

	var hdr zbootHeader
	if err := binary.Read(f, binary.LittleEndian, &hdr); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return kernelPath, nil
		}

		return "", fmt.Errorf("failed to read kernel %s: %w", kernelPath, err)
	}

	if string(hdr.MZ[:]) != "MZ" || string(hdr.Type[:]) != "zimg" {
		return kernelPath, nil
	}

	compression, _, _ := bytes.Cut(hdr.Compression[:], []byte{0})
	var decompressor func(dst io.Writer, src io.Reader) error
	switch string(compression) {
	case "gzip":
		decompressor = GzipDecompressor
	case "xzkern":
		decompressor = XzDecompressor
	case "zstd22":
		decompressor = ZstdDecompressor
	default:
		return "", fmt.Errorf("kernel %s is an EFI zboot image with unsupported compression %q",
			kernelPath, compression)
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

	payload := io.NewSectionReader(f, int64(hdr.PayloadOffset), int64(hdr.PayloadSize))
	if err := decompressor(out, payload); err != nil {
		return "", fmt.Errorf("failed to unpack EFI zboot kernel %s: %w", kernelPath, err)
	}

	return dst, nil
}

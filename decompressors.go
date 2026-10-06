package fakemachine

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// ZstdDecompressor is a cpio Transformer that writes the zstd-decompressed
// content of src to dst.
func ZstdDecompressor(dst io.Writer, src io.Reader) error {
	decompressor, err := zstd.NewReader(src)
	if err != nil {
		return fmt.Errorf("failed to create zstd decompressor: %w", err)
	}
	defer decompressor.Close()

	_, err = io.Copy(dst, decompressor)
	if err != nil {
		return fmt.Errorf("failed to decompress zstd data: %w", err)
	}

	return nil
}

// XzDecompressor is a cpio Transformer that writes the xz-decompressed
// content of src to dst.
func XzDecompressor(dst io.Writer, src io.Reader) error {
	decompressor, err := xz.NewReader(src)
	if err != nil {
		return fmt.Errorf("failed to create xz decompressor: %w", err)
	}
	// There is no Close() API. See: https://github.com/ulikunitz/xz/issues/45
	// defer decompressor.Close()

	_, err = io.Copy(dst, decompressor)
	if err != nil {
		return fmt.Errorf("failed to decompress xz data: %w", err)
	}

	return nil
}

// GzipDecompressor is a cpio Transformer that writes the gzip-decompressed
// content of src to dst.
func GzipDecompressor(dst io.Writer, src io.Reader) (err error) {
	decompressor, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("failed to create gzip decompressor: %w", err)
	}
	defer func() {
		if closeErr := decompressor.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close gzip decompressor: %w", closeErr))
		}
	}()

	//nolint:gosec // Input images are trusted and may legitimately expand without a fixed size limit.
	_, err = io.Copy(dst, decompressor)
	if err != nil {
		return fmt.Errorf("failed to decompress gzip data: %w", err)
	}

	return nil
}

// NullDecompressor is a cpio Transformer for uncompressed files; it copies
// src to dst unchanged.
func NullDecompressor(dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("failed to copy uncompressed data: %w", err)
	}

	return nil
}

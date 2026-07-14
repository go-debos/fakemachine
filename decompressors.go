package fakemachine

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// ZstdDecompressor decompresses zstd-compressed data from src into dst.
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

// XzDecompressor decompresses xz-compressed data from src into dst.
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

// GzipDecompressor decompresses gzip-compressed data from src into dst.
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

	_, err = io.Copy(dst, decompressor)
	if err != nil {
		return fmt.Errorf("failed to decompress gzip data: %w", err)
	}
	return nil
}

// NullDecompressor copies uncompressed data from src into dst unchanged.
func NullDecompressor(dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("failed to copy uncompressed data: %w", err)
	}
	return nil
}

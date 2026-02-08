package fakemachine

import (
	"compress/gzip"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

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

func XzDecompressor(dst io.Writer, src io.Reader) error {
	decompressor, err := xz.NewReader(src)
	if err != nil {
		return fmt.Errorf("failed to create xz decompressor: %w", err)
	}
	// There is no Close() API. See: https://github.com/ulikunitz/xz/issues/45
	//defer decompressor.Close()

	_, err = io.Copy(dst, decompressor)
	if err != nil {
		return fmt.Errorf("failed to decompress xz data: %w", err)
	}
	return nil
}

func GzipDecompressor(dst io.Writer, src io.Reader) error {
	decompressor, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("failed to create gzip decompressor: %w", err)
	}
	defer decompressor.Close()

	_, err = io.Copy(dst, decompressor)
	if err != nil {
		return fmt.Errorf("failed to decompress gzip data: %w", err)
	}
	return nil
}

func NullDecompressor(dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("failed to copy uncompressed data: %w", err)
	}
	return nil
}

package fakemachine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"testing"

	writerhelper "github.com/go-debos/fakemachine/cpio"
	"github.com/stretchr/testify/require"
)

func checkStreamsMatch(output, check io.Reader) error {
	outBytes, err := io.ReadAll(output)
	if err != nil {
		return fmt.Errorf("read output stream: %w", err)
	}
	checkBytes, err := io.ReadAll(check)
	if err != nil {
		return fmt.Errorf("read check stream: %w", err)
	}
	if !bytes.Equal(outBytes, checkBytes) {
		return fmt.Errorf("output (%d bytes) does not match expected (%d bytes)", len(outBytes), len(checkBytes))
	}
	return nil
}

func decompressorTest(suffix string, d writerhelper.Transformer) (err error) {
	testFilePath := path.Join("testdata", "test"+suffix)
	f, err := os.Open(testFilePath)
	if err != nil {
		return fmt.Errorf("open test file %s: %w", testFilePath, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close test file %s: %w", testFilePath, closeErr))
		}
	}()

	output := new(bytes.Buffer)
	if err := d(output, f); err != nil {
		return fmt.Errorf("decompress test file %s: %w", testFilePath, err)
	}

	checkFilePath := path.Join("testdata", "test")
	checkFile, err := os.Open(checkFilePath)
	if err != nil {
		return fmt.Errorf("open check file %s: %w", checkFilePath, err)
	}
	defer func() {
		if closeErr := checkFile.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close check file %s: %w", checkFilePath, closeErr))
		}
	}()

	if err := checkStreamsMatch(output, checkFile); err != nil {
		return fmt.Errorf("compare decompressed output: %w", err)
	}

	return nil
}

func TestZstd(t *testing.T) {
	err := decompressorTest(".zst", ZstdDecompressor)
	require.NoError(t, err)
}

func TestXz(t *testing.T) {
	err := decompressorTest(".xz", XzDecompressor)
	require.NoError(t, err)
}

func TestGzip(t *testing.T) {
	err := decompressorTest(".gz", GzipDecompressor)
	require.NoError(t, err)
}

func TestNull(t *testing.T) {
	err := decompressorTest("", NullDecompressor)
	require.NoError(t, err)
}

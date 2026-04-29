package fakemachine

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"testing"

	"github.com/go-debos/fakemachine/cpio"
	"github.com/stretchr/testify/require"
)

func checkStreamsMatch(output, check io.Reader) error {
	var i int64

	oreader := bufio.NewReader(output)
	creader := bufio.NewReader(check)

	for {
		ochar, oerr := oreader.ReadByte()
		if oerr != nil && !errors.Is(oerr, io.EOF) {
			return fmt.Errorf("read output stream at byte %d: %w", i, oerr)
		}

		cchar, cerr := creader.ReadByte()
		if cerr != nil && !errors.Is(cerr, io.EOF) {
			return fmt.Errorf("read check stream at byte %d: %w", i, cerr)
		}

		if errors.Is(oerr, io.EOF) || errors.Is(cerr, io.EOF) {
			switch {
			case errors.Is(oerr, io.EOF) && errors.Is(cerr, io.EOF):
				return nil
			case errors.Is(oerr, io.EOF):
				return fmt.Errorf("output stream shorter than check stream at byte %d", i)
			default:
				return fmt.Errorf("check stream shorter than output stream at byte %d", i)
			}
		}

		if ochar != cchar {
			return fmt.Errorf("data mismatch at byte %d: output=0x%02x check=0x%02x", i, ochar, cchar)
		}

		i++
	}
}

func decompressorTest(file, suffix string, d writerhelper.Transformer) (err error) {
	testFilePath := path.Join("testdata", file+suffix)
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

	checkFilePath := path.Join("testdata", file)
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
	err := decompressorTest("test", ".zst", ZstdDecompressor)
	require.NoError(t, err)
}

func TestXz(t *testing.T) {
	err := decompressorTest("test", ".xz", XzDecompressor)
	require.NoError(t, err)
}

func TestGzip(t *testing.T) {
	err := decompressorTest("test", ".gz", GzipDecompressor)
	require.NoError(t, err)
}

func TestNull(t *testing.T) {
	err := decompressorTest("test", "", NullDecompressor)
	require.NoError(t, err)
}

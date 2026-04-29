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

func decompressorTest(t *testing.T, file, suffix string, d writerhelper.Transformer) {
	f, err := os.Open(path.Join("testdata", file+suffix))
	if err != nil {
		t.Errorf("Unable to open test data: %s", err)
		return
	}
	defer f.Close()

	output := new(bytes.Buffer)
	err = d(output, f)
	if err != nil {
		t.Errorf("Error whilst decompressing test file: %s", err)
		return
	}

	checkFile, err := os.Open(path.Join("testdata", file))
	if err != nil {
		t.Errorf("Unable to open check data: %s", err)
		return
	}
	defer checkFile.Close()

	if err = checkStreamsMatch(output, checkFile); err != nil {
		t.Errorf("Failed to compare streams: %s", err)
		return
	}
}

func TestZstd(t *testing.T) {
	decompressorTest(t, "test", ".zst", ZstdDecompressor)
}

func TestXz(t *testing.T) {
	decompressorTest(t, "test", ".xz", XzDecompressor)
}

func TestGzip(t *testing.T) {
	decompressorTest(t, "test", ".gz", GzipDecompressor)
}

func TestNull(t *testing.T) {
	decompressorTest(t, "test", "", NullDecompressor)
}

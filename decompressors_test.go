package fakemachine

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"testing"

	"github.com/go-debos/fakemachine/cpio"
)

func checkStreamsMatch(t *testing.T, output, check io.Reader) error {
	i := 0
	oreader := bufio.NewReader(output)
	creader := bufio.NewReader(check)
	for {
		ochar, oerr := oreader.ReadByte()
		cchar, cerr := creader.ReadByte()
		if oerr != nil || cerr != nil {
			if oerr == io.EOF && cerr == io.EOF {
				return nil
			}
			if oerr != nil && oerr != io.EOF {
				t.Errorf("Error reading output stream: %s", oerr)
				return oerr
			}
			if cerr != nil && cerr != io.EOF {
				t.Errorf("Error reading check stream: %s", cerr)
				return cerr
			}
			return nil
		}

		if ochar != cchar {
			t.Errorf("Mismatch at byte %d, values %d (output) and %d (check)",
				i, ochar, cchar)
			return errors.New("Data mismatch")
		}
		i += 1
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

	check_f, err := os.Open(path.Join("testdata", file))
	if err != nil {
		t.Errorf("Unable to open check data: %s", err)
		return
	}
	defer check_f.Close()

	err = checkStreamsMatch(t, output, check_f)
	if err != nil {
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

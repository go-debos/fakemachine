package writerhelper

import (
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/surma/gocpio"
)

type WriterHelper struct {
	paths map[string]bool
	*cpio.Writer
}

func NewWriterHelper(f io.Writer) *WriterHelper {
	return &WriterHelper{
		paths:  map[string]bool{"/": true},
		Writer: cpio.NewWriter(f),
	}
}

func (w *WriterHelper) ensureBaseDirectory(directory string) {
	d := path.Clean(directory)

	if w.paths[d] {
		return
	}

	components := strings.Split(directory, "/")
	collector := "/"

	for _, c := range components {
		collector = path.Join(collector, c)
		if w.paths[collector] {
			continue
		}

		w.WriteDirectory(collector, 0755)
	}
}

func (w *WriterHelper) WriteDirectory(directory string, perm os.FileMode) {
	w.ensureBaseDirectory(path.Dir(directory))

	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_DIR
	hdr.Name = directory
	hdr.Mode = int64(perm)

	w.WriteHeader(hdr)

	w.paths[directory] = true
}

func (w *WriterHelper) WriteFile(file, content string, perm os.FileMode) {
	w.ensureBaseDirectory(path.Dir(file))

	hdr := new(cpio.Header)

	bytes := []byte(content)

	hdr.Type = cpio.TYPE_REG
	hdr.Name = file
	hdr.Mode = int64(perm)
	hdr.Size = int64(len(bytes))

	w.WriteHeader(hdr)
	w.Write(bytes)
}

func (w *WriterHelper) WriteSymlink(target, link string, perm os.FileMode) {
	w.ensureBaseDirectory(path.Dir(link))
	hdr := new(cpio.Header)

	content := []byte(target)

	hdr.Type = cpio.TYPE_SYMLINK
	hdr.Name = link
	hdr.Mode = int64(perm)
	hdr.Size = int64(len(content))

	w.WriteHeader(hdr)
	w.Write(content)
}

func (w *WriterHelper) WriteCharDevice(device string, major, minor int64,
	perm os.FileMode) {
	w.ensureBaseDirectory(path.Dir(device))
	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_CHAR
	hdr.Name = device
	hdr.Mode = int64(perm)
	hdr.Devmajor = major
	hdr.Devminor = minor

	w.WriteHeader(hdr)
}

func (w *WriterHelper) CopyTree(path string) {
	walker := func(p string, info os.FileInfo, err error) error {
		if info.Mode().IsDir() {
			w.WriteDirectory(p, info.Mode() & ^os.ModeType)
		} else if info.Mode().IsRegular() {
			w.CopyFile(p)
		} else {
			panic("No handled")
		}

		return nil
	}

	filepath.Walk(path, walker)
}

func (w *WriterHelper) CopyFileTo(src, dst string) error {
	w.ensureBaseDirectory(path.Dir(dst))

	f, err := os.Open(src)
	if err != nil {
		log.Panicf("open failed: %s - %v", src, err)
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_REG
	hdr.Name = dst
	hdr.Mode = int64(info.Mode() & ^os.ModeType)
	hdr.Size = info.Size()

	w.WriteHeader(hdr)
	io.Copy(w, f)

	return nil
}

func (w *WriterHelper) CopyFile(in string) error {
	return w.CopyFileTo(in, in)
}

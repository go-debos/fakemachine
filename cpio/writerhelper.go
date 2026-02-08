package writerhelper

import (
	"bytes"
	"fmt"
	"io"
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

type WriteDirectory struct {
	Directory string
	Perm      os.FileMode
}

type WriteSymlink struct {
	Target string
	Link   string
	Perm   os.FileMode
}

type Transformer func(dst io.Writer, src io.Reader) error

func NewWriterHelper(f io.Writer) *WriterHelper {
	return &WriterHelper{
		paths:  map[string]bool{"/": true},
		Writer: cpio.NewWriter(f),
	}
}

func (w *WriterHelper) ensureBaseDirectory(directory string) error {
	d := path.Clean(directory)

	if w.paths[d] {
		return nil
	}

	components := strings.Split(directory, "/")
	collector := "/"

	for _, c := range components {
		collector = path.Join(collector, c)
		if w.paths[collector] {
			continue
		}

		err := w.WriteDirectory(collector, 0755)
		if err != nil {
			return err
		}
	}

	return nil
}

func (w *WriterHelper) WriteDirectories(directories []WriteDirectory) error {
	for _, d := range directories {
		err := w.WriteDirectory(d.Directory, d.Perm)
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *WriterHelper) WriteDirectory(directory string, perm os.FileMode) error {
	err := w.ensureBaseDirectory(path.Dir(directory))
	if err != nil {
		return err
	}

	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_DIR
	hdr.Name = directory
	hdr.Mode = int64(perm)

	err = w.WriteHeader(hdr)
	if err != nil {
		return fmt.Errorf("failed to write directory header: %w", err)
	}

	w.paths[directory] = true
	return nil
}

func (w *WriterHelper) WriteFile(file, content string, perm os.FileMode) error {
	return w.WriteFileRaw(file, []byte(content), perm)
}

func (w *WriterHelper) WriteFileRaw(file string, bytes []byte, perm os.FileMode) error {
	err := w.ensureBaseDirectory(path.Dir(file))
	if err != nil {
		return err
	}

	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_REG
	hdr.Name = file
	hdr.Mode = int64(perm)
	hdr.Size = int64(len(bytes))

	err = w.WriteHeader(hdr)
	if err != nil {
		return fmt.Errorf("failed to write file header: %w", err)
	}
	_, err = w.Write(bytes)
	if err != nil {
		return fmt.Errorf("failed to write file content: %w", err)
	}
	return nil
}

func (w *WriterHelper) WriteSymlinks(links []WriteSymlink) error {
	for _, l := range links {
		err := w.WriteSymlink(l.Target, l.Link, l.Perm)
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *WriterHelper) WriteSymlink(target, link string, perm os.FileMode) error {
	err := w.ensureBaseDirectory(path.Dir(link))
	if err != nil {
		return err
	}

	hdr := new(cpio.Header)

	content := []byte(target)

	hdr.Type = cpio.TYPE_SYMLINK
	hdr.Name = link
	hdr.Mode = int64(perm)
	hdr.Size = int64(len(content))

	err = w.WriteHeader(hdr)
	if err != nil {
		return fmt.Errorf("failed to write symlink header: %w", err)
	}

	_, err = w.Write(content)
	if err != nil {
		return fmt.Errorf("failed to write symlink content: %w", err)
	}
	return nil
}

func (w *WriterHelper) WriteCharDevice(device string, major, minor int64, perm os.FileMode) error {
	err := w.ensureBaseDirectory(path.Dir(device))
	if err != nil {
		return err
	}
	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_CHAR
	hdr.Name = device
	hdr.Mode = int64(perm)
	hdr.Devmajor = major
	hdr.Devminor = minor

	err = w.WriteHeader(hdr)
	if err != nil {
		return fmt.Errorf("failed to write character device header: %w", err)
	}
	return nil
}

func (w *WriterHelper) CopyTree(path string) error {
	walker := func(p string, info os.FileInfo, _ error) error {
		var err error
		if info.Mode().IsDir() {
			err = w.WriteDirectory(p, info.Mode() & ^os.ModeType)
		} else if info.Mode().IsRegular() {
			err = w.CopyFile(p)
		} else {
			err = fmt.Errorf("file type not handled for %s", p)
		}

		return err
	}

	err := filepath.Walk(path, walker)
	if err != nil {
		return fmt.Errorf("failed to walk directory %s: %w", path, err)
	}
	return nil
}

func (w *WriterHelper) CopyFileTo(src, dst string) error {
	err := w.ensureBaseDirectory(path.Dir(dst))
	if err != nil {
		return err
	}

	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open failed: %s - %w", src, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source file %s: %w", src, err)
	}

	hdr := new(cpio.Header)

	hdr.Type = cpio.TYPE_REG
	hdr.Name = dst
	hdr.Mode = int64(info.Mode() & ^os.ModeType)
	hdr.Size = info.Size()

	err = w.WriteHeader(hdr)
	if err != nil {
		return fmt.Errorf("failed to write file header for %s: %w", dst, err)
	}

	_, err = io.Copy(w, f)
	if err != nil {
		return fmt.Errorf("failed to copy file content: %w", err)
	}

	return nil
}

func (w *WriterHelper) TransformFileTo(src, dst string, fn Transformer) error {
	err := w.ensureBaseDirectory(path.Dir(dst))
	if err != nil {
		return err
	}

	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source file %s: %w", src, err)
	}

	out := new(bytes.Buffer)
	err = fn(out, f)
	if err != nil {
		return err
	}

	hdr := new(cpio.Header)
	hdr.Type = cpio.TYPE_REG
	hdr.Name = dst
	hdr.Mode = int64(info.Mode() & ^os.ModeType)
	hdr.Size = int64(out.Len())

	err = w.WriteHeader(hdr)
	if err != nil {
		return fmt.Errorf("failed to write header for transformed file %s: %w", dst, err)
	}

	_, err = io.Copy(w, out)
	if err != nil {
		return fmt.Errorf("failed to copy transformed content: %w", err)
	}

	return nil
}

func (w *WriterHelper) CopyFile(in string) error {
	return w.CopyFileTo(in, in)
}

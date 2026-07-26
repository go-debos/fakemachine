// Package writerhelper builds cpio archives, such as the fakemachine initrd,
// from in-memory content and files copied from the host.
package writerhelper

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	cpio "github.com/surma/gocpio"
)

// WriterHelper wraps a cpio.Writer and remembers which directories have been
// written, so every entry's missing parent directories are created (mode
// 0755) before the entry itself.
type WriterHelper struct {
	*cpio.Writer

	paths map[string]bool
}

// WriteDirectory describes a directory for WriterHelper.WriteDirectories.
type WriteDirectory struct {
	Directory string
	Perm      os.FileMode
}

// WriteSymlink describes a symlink at Link pointing to Target for
// WriterHelper.WriteSymlinks.
type WriteSymlink struct {
	Target string
	Link   string
	Perm   os.FileMode
}

// Transformer reads data from src and writes a modified version of it, for
// example decompressed, to dst. It is used by WriterHelper.TransformFileTo.
type Transformer func(dst io.Writer, src io.Reader) error

// NewWriterHelper returns a WriterHelper writing a cpio archive to f. The
// caller must call Close to write the archive trailer.
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

		err := w.WriteDirectory(collector, 0o755)
		if err != nil {
			return err
		}
	}

	return nil
}

// WriteDirectories calls WriteDirectory for each entry, stopping at the
// first error.
func (w *WriterHelper) WriteDirectories(directories []WriteDirectory) error {
	for _, d := range directories {
		err := w.WriteDirectory(d.Directory, d.Perm)
		if err != nil {
			return err
		}
	}

	return nil
}

// WriteDirectory adds the directory with the given permissions, creating any
// missing parent directories first.
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

// WriteFile adds a regular file containing content; see WriteFileRaw.
func (w *WriterHelper) WriteFile(file, content string, perm os.FileMode) error {
	return w.WriteFileRaw(file, []byte(content), perm)
}

// WriteFileRaw adds a regular file containing bytes with the given
// permissions, creating any missing parent directories first.
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

// WriteSymlinks calls WriteSymlink for each entry, stopping at the first
// error.
func (w *WriterHelper) WriteSymlinks(links []WriteSymlink) error {
	for _, l := range links {
		err := w.WriteSymlink(l.Target, l.Link, l.Perm)
		if err != nil {
			return err
		}
	}

	return nil
}

// WriteSymlink adds a symlink at link pointing to target, creating any
// missing parent directories of link first. The target is not checked.
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

// WriteCharDevice adds a character device node with the given major and
// minor numbers, creating any missing parent directories first.
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

// CopyTree copies the host directory tree at path into the archive at the
// same location, keeping permissions. Only directories and regular files are
// supported; any other file type, such as a symlink, returns an error.
func (w *WriterHelper) CopyTree(path string) error {
	walker := func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("error visiting %s: %w", p, err)
		}
		switch {
		case info.Mode().IsDir():
			err = w.WriteDirectory(p, info.Mode() & ^os.ModeType)
		case info.Mode().IsRegular():
			err = w.CopyFile(p)
		default:
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

// CopyFileTo copies the host file src into the archive as dst, keeping its
// permissions and creating any missing parent directories of dst first.
func (w *WriterHelper) CopyFileTo(src, dst string) (err error) {
	if err := w.ensureBaseDirectory(path.Dir(dst)); err != nil {
		return err
	}

	//nolint:gosec // src is an explicit input path that this method is expected to open.
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open failed: %s - %w", src, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close source file %s: %w", src, closeErr))
		}
	}()

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

// TransformFileTo is like CopyFileTo, but passes the content of src through
// fn before writing it to dst. The transformed content is buffered in memory.
func (w *WriterHelper) TransformFileTo(src, dst string, fn Transformer) (err error) {
	if err := w.ensureBaseDirectory(path.Dir(dst)); err != nil {
		return err
	}

	//nolint:gosec // src is an explicit input path that this method is expected to transform.
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close source file %s: %w", src, closeErr))
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat source file %s: %w", src, err)
	}

	out := new(bytes.Buffer)
	err = fn(out, f)
	if err != nil {
		return fmt.Errorf("failed to transform source file %s: %w", src, err)
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

// CopyFile copies the host file in into the archive at the same path; see
// CopyFileTo.
func (w *WriterHelper) CopyFile(in string) error {
	return w.CopyFileTo(in, in)
}

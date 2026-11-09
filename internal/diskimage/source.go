package diskimage

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/ulikunitz/xz"
)

// Format is how a source image file is stored.
type Format string

const (
	FormatRaw  Format = "img"
	FormatXZ   Format = "xz"
	FormatGzip Format = "gz"
	FormatZip  Format = "zip"
)

// Source streams a disk image, decompressing on the fly.
type Source struct {
	Path   string
	Format Format
	// FileSize is the size of the file on disk (compressed size for xz/gz/zip).
	FileSize int64
	// ImageSize is the uncompressed image size, or -1 when unknown until the end (xz, gz).
	ImageSize int64

	file   *os.File
	read   atomic.Int64 // bytes of the file consumed, for progress
	reader io.Reader
	closer []io.Closer
}

// Open detects the format from the file's first bytes, not its name.
func Open(path string) (*Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.IsDir() {
		f.Close()
		return nil, fmt.Errorf("%s is a directory", path)
	}
	head := make([]byte, 8)
	n, _ := f.ReadAt(head, 0)
	head = head[:n]
	s := &Source{Path: path, FileSize: st.Size(), ImageSize: -1, file: f, closer: []io.Closer{f}}
	counted := &countingReader{r: f, n: &s.read}

	switch {
	case bytes.HasPrefix(head, []byte{0xFD, '7', 'z', 'X', 'Z', 0x00}):
		s.Format = FormatXZ
		zr, err := xz.NewReader(bufio.NewReaderSize(counted, 1<<20))
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("open xz: %w", err)
		}
		s.reader = zr
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		s.Format = FormatGzip
		gr, err := gzip.NewReader(bufio.NewReaderSize(counted, 1<<20))
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("open gzip: %w", err)
		}
		s.closer = append(s.closer, gr)
		s.reader = gr
	case bytes.HasPrefix(head, []byte{'P', 'K', 0x03, 0x04}):
		s.Format = FormatZip
		zr, err := zip.NewReader(f, st.Size())
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("open zip: %w", err)
		}
		entry, err := pickZipEntry(zr)
		if err != nil {
			s.Close()
			return nil, err
		}
		rc, err := entry.Open()
		if err != nil {
			s.Close()
			return nil, err
		}
		s.closer = append(s.closer, rc)
		s.ImageSize = int64(entry.UncompressedSize64)
		// zip reads the file through its own ReaderAt; count decompressed bytes
		// against the uncompressed size instead.
		s.reader = &countingReader{r: rc, n: &s.read}
		s.FileSize = s.ImageSize
	default:
		s.Format = FormatRaw
		s.ImageSize = st.Size()
		s.reader = counted
	}
	return s, nil
}

func pickZipEntry(zr *zip.Reader) (*zip.File, error) {
	var images []*zip.File
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && strings.HasSuffix(strings.ToLower(f.Name), ".img") {
			images = append(images, f)
		}
	}
	switch len(images) {
	case 1:
		return images[0], nil
	case 0:
		return nil, errors.New("the zip file contains no .img file")
	default:
		return nil, errors.New("the zip file contains more than one .img file")
	}
}

// Read returns uncompressed image bytes.
func (s *Source) Read(p []byte) (int, error) { return s.reader.Read(p) }

// Progress returns how far through the source file reading is, from 0 to 1.
func (s *Source) Progress() float64 {
	if s.FileSize <= 0 {
		return 0
	}
	v := float64(s.read.Load()) / float64(s.FileSize)
	if v > 1 {
		v = 1
	}
	return v
}

// Close releases the file and decompressors.
func (s *Source) Close() error {
	var first error
	for i := len(s.closer) - 1; i >= 0; i-- {
		if err := s.closer[i].Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

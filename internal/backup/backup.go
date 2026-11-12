// Package backup reads a card into an image file, the reverse of a flash. The
// copy stops at the end of the last partition, and with e2fsprogs installed the
// root filesystem is shrunk so the image fits smaller cards (MeteorCloud's
// prepared images grow it back on first boot).
package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/meteor-edge/meteor-imager/internal/diskimage"
)

// Stages, in order. Shrink runs only when it is possible.
const (
	StageRead   = "read"
	StageShrink = "shrink"
	StageSave   = "save"
)

const chunk = 4 << 20

// Options tune a backup; the zero value copies without shrinking.
type Options struct {
	// Shrink makes the last ext4 partition as small as its data (plus headroom)
	// using e2fsck and resize2fs. Skipped with a note when not possible.
	Shrink   bool
	Progress func(stage string, fraction float64, bytes int64)
}

// Report is the outcome.
type Report struct {
	Path string `json:"path"`
	// ImageBytes is the uncompressed image size; FileBytes what is on disk.
	ImageBytes int64    `json:"image_bytes"`
	FileBytes  int64    `json:"file_bytes"`
	SHA256     string   `json:"sha256"`
	Shrunk     bool     `json:"shrunk"`
	Notes      []string `json:"notes,omitempty"`
}

// CheckOutput validates an output path before the card is opened.
func CheckOutput(path string) error {
	if !strings.HasSuffix(path, ".img") && !strings.HasSuffix(path, ".img.gz") {
		return errors.New("the backup file name must end in .img or .img.gz")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; choose a new file name", path)
	}
	dir := filepath.Dir(path)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("folder %s does not exist", dir)
	}
	return nil
}

// ShrinkAvailable reports whether e2fsprogs is installed.
func ShrinkAvailable() bool {
	for _, tool := range []string{"e2fsck", "resize2fs", "dumpe2fs"} {
		if _, err := lookTool(tool); err != nil {
			return false
		}
	}
	return true
}

// lookTool finds an e2fsprogs tool, also in sbin and Homebrew's keg-only prefix.
func lookTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range []string{"/sbin", "/usr/sbin", "/opt/homebrew/opt/e2fsprogs/sbin", "/usr/local/opt/e2fsprogs/sbin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found", name)
}

// Run copies src (a card of size bytes) to outPath (.img or .img.gz).
func Run(ctx context.Context, src io.ReaderAt, size int64, outPath string, opt Options) (*Report, error) {
	if err := CheckOutput(outPath); err != nil {
		return nil, err
	}
	progress := opt.Progress
	if progress == nil {
		progress = func(string, float64, int64) {}
	}
	rep := &Report{Path: outPath}

	end := size
	parts, err := diskimage.ReadPartitions(src)
	if err != nil {
		rep.Notes = append(rep.Notes, "No partition table found; copying the whole card.")
	} else {
		end = 0
		for _, p := range parts {
			end = max(end, p.Offset()+p.Size())
		}
		if end > size || end <= 0 {
			end = size
		}
	}

	var root *diskimage.Partition
	if opt.Shrink {
		root, err = shrinkable(parts)
		if err == nil && !ShrinkAvailable() {
			err = errors.New("e2fsprogs (e2fsck, resize2fs) is not installed")
		}
		if err != nil {
			rep.Notes = append(rep.Notes, "Not shrunk: "+err.Error()+". The image is as large as the card's partitions.")
			root = nil
		}
	}

	tmp := outPath + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	hash := sha256.New()
	counter := &countWriter{w: io.MultiWriter(f, hash)}
	var out io.Writer = counter
	var gz *gzip.Writer
	if strings.HasSuffix(outPath, ".gz") {
		gz = gzip.NewWriter(counter)
		out = gz
	}

	if root != nil {
		shrunk, err := writeShrunk(ctx, src, *root, filepath.Dir(outPath), out, progress)
		if err != nil {
			return nil, err
		}
		if shrunk > 0 {
			rep.Shrunk, rep.ImageBytes = true, shrunk
		} else {
			rep.Notes = append(rep.Notes, "Not shrunk: the root filesystem is already about as small as its data.")
		}
	}
	if !rep.Shrunk {
		if err := copyRange(ctx, src, 0, end, out, func(done int64) {
			progress(StageRead, float64(done)/float64(end), done)
		}); err != nil {
			return nil, err
		}
		rep.ImageBytes = end
	}

	progress(StageSave, 0, 0)
	if gz != nil {
		if err := gz.Close(); err != nil {
			return nil, err
		}
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, outPath); err != nil {
		return nil, err
	}
	ok = true
	rep.FileBytes = counter.n
	rep.SHA256 = hex.EncodeToString(hash.Sum(nil))
	progress(StageSave, 1, rep.FileBytes)
	return rep, nil
}

// shrinkable returns the partition to shrink: the last one, Linux (MBR 83).
func shrinkable(parts []diskimage.Partition) (*diskimage.Partition, error) {
	if len(parts) == 0 {
		return nil, errors.New("no partition table")
	}
	last := parts[0]
	for _, p := range parts[1:] {
		if p.Offset() > last.Offset() {
			last = p
		}
	}
	if len(last.Type) != 2 {
		return nil, errors.New("GPT cards are not shrunk")
	}
	if last.Type != "83" {
		return nil, fmt.Errorf("the last partition is type %s, not Linux", last.Type)
	}
	return &last, nil
}

// writeShrunk copies the root partition to a temporary file in tmpDir, shrinks
// it there (never on the card), and writes the start of the card, with the
// partition table updated, followed by the shrunk partition. It returns the
// image size, or 0 when shrinking would not help (nothing was written to out).
func writeShrunk(ctx context.Context, src io.ReaderAt, root diskimage.Partition, tmpDir string, out io.Writer, progress func(string, float64, int64)) (int64, error) {
	// Next to the output: that disk has room for the backup.
	dir, err := os.MkdirTemp(tmpDir, ".meteor-backup-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	img := filepath.Join(dir, "root.img")
	rf, err := os.OpenFile(img, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	defer rf.Close()
	if err := copyRange(ctx, src, root.Offset(), root.Offset()+root.Size(), &sparseWriter{f: rf}, func(done int64) {
		progress(StageRead, float64(done)/float64(root.Size()), done)
	}); err != nil {
		return 0, err
	}
	if err := rf.Truncate(root.Size()); err != nil {
		return 0, err
	}
	if err := rf.Close(); err != nil {
		return 0, err
	}

	progress(StageShrink, 0, 0)
	newSize, err := shrinkFS(ctx, img, root.Size())
	if err != nil {
		return 0, err
	}
	if newSize == 0 {
		return 0, nil
	}
	progress(StageShrink, 1, newSize)

	mbr := make([]byte, diskimage.SectorSize)
	if _, err := src.ReadAt(mbr, 0); err != nil {
		return 0, fmt.Errorf("read the card: %w", err)
	}
	if err := setMBRSectors(mbr, root.Index, uint32(newSize/diskimage.SectorSize)); err != nil {
		return 0, err
	}
	if _, err := out.Write(mbr); err != nil {
		return 0, err
	}
	if err := copyRange(ctx, src, diskimage.SectorSize, root.Offset(), out, func(int64) {}); err != nil {
		return 0, err
	}
	shrunk, err := os.Open(img)
	if err != nil {
		return 0, err
	}
	defer shrunk.Close()
	if err := copyRange(ctx, shrunk, 0, newSize, out, func(done int64) {
		progress(StageSave, float64(done)/float64(newSize), done)
	}); err != nil {
		return 0, err
	}
	return root.Offset() + newSize, nil
}

var (
	minBlocksRe = regexp.MustCompile(`(?m)minimum size of the filesystem:\s*(\d+)`)
	blockSizeRe = regexp.MustCompile(`(?m)^Block size:\s*(\d+)`)
	blockCount  = regexp.MustCompile(`(?m)^Block count:\s*(\d+)`)
)

// shrinkFS shrinks the ext filesystem in img to its minimum plus headroom and
// returns the new size in bytes, or 0 if that would not save at least 10%.
func shrinkFS(ctx context.Context, img string, current int64) (int64, error) {
	run := func(name string, args ...string) (string, int, error) {
		bin, err := lookTool(name)
		if err != nil {
			return "", -1, err
		}
		cmd := exec.CommandContext(ctx, bin, args...)
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		err = cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code, err = ee.ExitCode(), nil
		}
		return buf.String(), code, err
	}

	out, code, err := run("e2fsck", "-fy", img)
	if err != nil {
		return 0, err
	}
	// 0: clean, 1: errors fixed, 2: fixed, reboot advised (irrelevant for a file).
	if code > 2 {
		return 0, fmt.Errorf("e2fsck found errors it could not fix (exit %d): %s", code, lastLine(out))
	}
	out, code, err = run("dumpe2fs", "-h", img)
	if err != nil || code != 0 {
		return 0, fmt.Errorf("dumpe2fs: %v %s", err, lastLine(out))
	}
	bs, count := atoiMatch(blockSizeRe, out), atoiMatch(blockCount, out)
	if bs <= 0 || count <= 0 {
		return 0, errors.New("dumpe2fs: no block size or count")
	}
	out, code, err = run("resize2fs", "-P", img)
	if err != nil || code != 0 {
		return 0, fmt.Errorf("resize2fs -P: %v %s", err, lastLine(out))
	}
	minBlocks := atoiMatch(minBlocksRe, out)
	if minBlocks <= 0 {
		return 0, errors.New("resize2fs -P: no minimum size")
	}
	// Headroom so the system still boots if the filesystem is not grown back.
	target := minBlocks + max(minBlocks/10, (256<<20)/bs)
	// Whole 4 MiB units keep the partition end aligned.
	align := int64(4<<20) / bs
	target = (target + align - 1) / align * align
	if target*10 > count*9 || target*bs > current {
		return 0, nil
	}
	out, code, err = run("resize2fs", img, strconv.FormatInt(target, 10))
	if err != nil || code != 0 {
		return 0, fmt.Errorf("resize2fs: %v %s", err, lastLine(out))
	}
	return target * bs, nil
}

func atoiMatch(re *regexp.Regexp, s string) int64 {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	return n
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// setMBRSectors sets the sector count of MBR entry index (1-based) in the first sector.
func setMBRSectors(head []byte, index int, sectors uint32) error {
	if len(head) < diskimage.SectorSize || index < 1 || index > 4 {
		return errors.New("cannot update the partition table")
	}
	e := head[446+16*(index-1):]
	binary.LittleEndian.PutUint32(e[12:16], sectors)
	return nil
}

func copyRange(ctx context.Context, src io.ReaderAt, from, to int64, out io.Writer, done func(int64)) error {
	buf := make([]byte, chunk)
	for off := from; off < to; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(int64(len(buf)), to-off)
		read, err := src.ReadAt(buf[:n], off)
		if int64(read) != n {
			if err == nil || errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("read at %d: %w", off, err)
		}
		if _, err := out.Write(buf[:n]); err != nil {
			return err
		}
		off += n
		done(off - from)
	}
	return nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// sparseWriter skips all-zero chunks, so the temporary copy of a mostly empty
// partition takes little disk space.
type sparseWriter struct {
	f   *os.File
	off int64
}

func (s *sparseWriter) Write(p []byte) (int, error) {
	if !allZero(p) {
		if _, err := s.f.WriteAt(p, s.off); err != nil {
			return 0, err
		}
	}
	s.off += int64(len(p))
	return len(p), nil
}

func allZero(p []byte) bool {
	for _, b := range p {
		if b != 0 {
			return false
		}
	}
	return true
}

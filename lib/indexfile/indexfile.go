// Package indexfile owns the on-disk framing shared by every serialized
// index (S2, name, postal code):
//
//	gob(header)                // uncompressed: version checks run before any decompression
//	zstd-frame(gob(payload))   // one frame, CRC on
//
// Write is atomic and durable: the bytes land in path+".part", are fsynced,
// and only then renamed over path (and the directory fsynced), so a reader
// never observes a half-written index and a crash never leaves one behind.
//
// Reader streams the payload straight from the file through the zstd decoder
// into gob. Decoding a whole-file buffer instead held the compressed bytes,
// the decompressed bytes and the decoded structure at once: at production
// scale, hundreds of MB of transient peak per index at boot. The stream is
// still fully verified: after the payload decodes, the rest of the frame is
// drained, which checks the frame CRC and rejects trailing bytes.
//
// Header and payload types belong to the callers, as do their versions and
// their corruption errors: every framing or decode failure here wraps
// ErrFormat, which callers translate into their own ErrCorruptIndex.
package indexfile

import (
	"bufio"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"
)

// ErrFormat reports a file whose framing or gob content cannot be decoded:
// truncated, corrupted (CRC), trailing bytes, or values of the wrong shape.
var ErrFormat = errors.New("index file cannot be decoded")

// zstdLevel trades ratio for encode speed; decompression speed is nearly
// level-independent.
const zstdLevel = zstd.SpeedFastest

// Write atomically writes header and payload to path (see the package
// comment). On any failure the .part file is removed and path is untouched.
func Write(path string, header, payload any) (err error) {
	partPath := path + ".part"
	file, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", partPath, err)
	}
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(partPath)
		}
	}()
	if err := gob.NewEncoder(file).Encode(header); err != nil {
		return fmt.Errorf("encode header: %w", err)
	}
	zw, err := zstd.NewWriter(file, zstd.WithEncoderLevel(zstdLevel), zstd.WithEncoderCRC(true))
	if err != nil {
		return fmt.Errorf("create zstd writer: %w", err)
	}
	if err := gob.NewEncoder(zw).Encode(payload); err != nil {
		_ = zw.Close()
		return fmt.Errorf("encode payload: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finalize zstd frame: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", partPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", partPath, err)
	}
	if err := os.Rename(partPath, path); err != nil {
		return fmt.Errorf("move %s to %s: %w", partPath, path, err)
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir makes a rename in dir durable. Best effort: some platforms and
// filesystems refuse to sync a directory, and the file itself is already
// synced, so a failure here is not worth failing the write for.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// Stats describes one payload decode, for the callers' boot logs.
type Stats struct {
	FileBytes    int64         // compressed frame bytes read
	PayloadBytes int64         // decompressed gob bytes
	Decode       time.Duration // zstd + gob, streamed together
}

// Reader reads one index file: Header first, then Payload, then Close.
type Reader struct {
	file  *os.File
	buf   *bufio.Reader
	stats Stats
}

// Open opens path for reading. Errors are environmental (not ErrFormat).
func Open(path string) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &Reader{file: file, buf: bufio.NewReader(file)}, nil
}

// Header decodes the uncompressed header into v. The bufio.Reader is a
// ByteReader, so gob consumes exactly the header's bytes and leaves the
// frame intact.
func (r *Reader) Header(v any) error {
	if err := gob.NewDecoder(r.buf).Decode(v); err != nil {
		return fmt.Errorf("%w: header: %v", ErrFormat, err)
	}
	return nil
}

// Payload streams the zstd frame into v and verifies the frame to its end.
func (r *Reader) Payload(v any) error {
	start := time.Now()
	compressed := &countingReader{r: r.buf}
	zr, err := zstd.NewReader(compressed, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close() // release the decoder's window and buffers right away
	raw := &countingReader{r: zr}
	// Own the gob-side buffer: gob would otherwise add a hidden one that
	// could swallow trailing bytes before the drain below sees them.
	payload := bufio.NewReaderSize(raw, 1<<16)
	if err := gob.NewDecoder(payload).Decode(v); err != nil {
		return fmt.Errorf("%w: payload: %v", ErrFormat, err)
	}
	// Drain to EOF: the decoder verifies the frame CRC only there, and any
	// bytes after the gob value mean the file is not what it claims.
	if n, err := io.Copy(io.Discard, payload); err != nil {
		return fmt.Errorf("%w: payload frame: %v", ErrFormat, err)
	} else if n > 0 {
		return fmt.Errorf("%w: %d trailing bytes after the payload", ErrFormat, n)
	}
	r.stats = Stats{FileBytes: compressed.n, PayloadBytes: raw.n, Decode: time.Since(start)}
	return nil
}

// Stats reports the last successful Payload decode.
func (r *Reader) Stats() Stats { return r.stats }

// Close closes the file.
func (r *Reader) Close() error { return r.file.Close() }

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

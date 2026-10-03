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
//
// A hostile or corrupt file is bounded on the read side, with no change to
// the format: the zstd window is capped far above what Write produces, and
// the decompressed payload may not exceed a byte budget (see
// DefaultMaxPayloadBytes and Reader.LimitPayload for how it is chosen).
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

// MaxDecoderWindow caps the zstd window a Reader accepts (and, by
// WithDecoderMaxMemory, the memory the decoder may use). The decoder
// allocates the declared window before reading any data, so a 9-byte frame
// declaring the library default of 512 MB would otherwise cost 512 MB per
// attempt. Write produces a 4 MiB window (zstdLevel's default; a test pins
// it by reading the frame header), so this is 16x headroom.
const MaxDecoderWindow = 64 << 20

// DefaultMaxPayloadBytes is the budget for decompressed payload bytes when a
// caller does not set one with Reader.LimitPayload. It equals gob's own
// ceiling for one message (1 GiB), so it never rejects a payload gob could
// decode anyway; production indexes are far below it (a 13M-city S2 index
// is roughly 0.7 GB decompressed). It is a variable so a deployment or a
// test can tighten it.
//
// A ratio cap (decompressed/compressed) was measured and rejected: real
// indexes compress 1.6-2.3x, but legitimate highly repetitive data (200k
// cities with identical name and coordinates) reaches 3900x and approaches
// 10000x at scale, which overlaps the 2000-5000x of a bomb made of
// one-byte-per-entry records. Only the caller knows how many entries the
// header promises, so a caller that wants a tight bound passes it to
// LimitPayload.
var DefaultMaxPayloadBytes int64 = 1 << 30

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
	file       *os.File
	buf        *bufio.Reader
	stats      Stats
	maxPayload int64
}

// Open opens path for reading. Errors are environmental (not ErrFormat).
func Open(path string) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &Reader{file: file, buf: bufio.NewReader(file), maxPayload: DefaultMaxPayloadBytes}, nil
}

// LimitPayload sets the most decompressed payload bytes Payload will accept
// (the default is DefaultMaxPayloadBytes); beyond it Payload fails with
// ErrFormat before the rest is inflated. A caller that knows how many
// entries the header declares should pass a bound derived from that count,
// since gob allocates one in-memory element per few payload bytes.
func (r *Reader) LimitPayload(maxBytes int64) { r.maxPayload = maxBytes }

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
	zr, err := zstd.NewReader(compressed,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxWindow(MaxDecoderWindow),
		zstd.WithDecoderMaxMemory(MaxDecoderWindow))
	if err != nil {
		return fmt.Errorf("create zstd reader: %w", err)
	}
	defer zr.Close() // release the decoder's window and buffers right away
	raw := &countingReader{r: &limitReader{r: zr, left: r.maxPayload}}
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

// limitReader fails once more than left bytes have been read through it, so
// a decompression bomb stops inflating at the budget.
type limitReader struct {
	r    io.Reader
	left int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, errPayloadTooLarge
	}
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1] // one byte past the budget is enough to detect it
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return 0, errPayloadTooLarge
	}
	return n, err
}

var errPayloadTooLarge = errors.New("decompressed payload exceeds the size budget")

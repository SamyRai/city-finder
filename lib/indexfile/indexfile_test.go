package indexfile

import (
	"bytes"
	"encoding/gob"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

type testHeader struct {
	Magic   string
	Version uint32
}

type testPayload struct {
	Names []string
	IDs   []int32
}

func samplePayload() testPayload {
	p := testPayload{}
	for i := range 5000 {
		p.Names = append(p.Names, "name-"+string(rune('a'+i%26)))
		p.IDs = append(p.IDs, int32(i))
	}
	return p
}

// read decodes path with the Reader, the way the index packages do.
func read(t *testing.T, path string) (testHeader, testPayload, Stats, error) {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var h testHeader
	var p testPayload
	if err := r.Header(&h); err != nil {
		return h, p, Stats{}, err
	}
	err = r.Payload(&p)
	return h, p, r.Stats(), err
}

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.gob")
	want := samplePayload()
	if err := Write(path, testHeader{"MAGIC", 3}, &want); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part must be gone after a successful write, stat err = %v", err)
	}
	h, got, stats, err := read(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if h != (testHeader{"MAGIC", 3}) || len(got.Names) != len(want.Names) || got.IDs[4999] != 4999 {
		t.Fatalf("round trip mismatch: header %+v, %d names", h, len(got.Names))
	}
	fi, _ := os.Stat(path)
	if stats.FileBytes <= 0 || stats.FileBytes >= fi.Size() || stats.PayloadBytes <= stats.FileBytes {
		t.Fatalf("implausible stats %+v for a %d-byte file", stats, fi.Size())
	}
}

// TestReadRejectsDamagedFiles: every damage class decodes to ErrFormat.
func TestReadRejectsDamagedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.gob")
	payload := samplePayload()
	if err := Write(path, testHeader{"MAGIC", 3}, &payload); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	check := func(name string, data []byte) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := read(t, p); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", name, err)
		}
	}

	for _, n := range []int{0, 1, 10, len(good) / 2, len(good) - 4, len(good) - 1} {
		check("truncated", good[:n])
	}
	flipped := bytes.Clone(good)
	flipped[len(flipped)-6] ^= 0xFF // inside the frame's last block or CRC
	check("flipped", flipped)
	check("garbage-after-frame", append(bytes.Clone(good), 0xde, 0xad, 0xbe, 0xef))

	// Extra bytes INSIDE a valid frame, after the gob value.
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(testHeader{"MAGIC", 3}); err != nil {
		t.Fatal(err)
	}
	var inner bytes.Buffer
	if err := gob.NewEncoder(&inner).Encode(&payload); err != nil {
		t.Fatal(err)
	}
	inner.WriteString("trailing")
	zw, _ := zstd.NewWriter(&buf, zstd.WithEncoderCRC(true))
	_, _ = zw.Write(inner.Bytes())
	_ = zw.Close()
	check("trailing-in-frame", buf.Bytes())
}

// TestWriteFailureLeavesNoPart: an unencodable payload fails the write,
// removes the .part file and leaves an existing index untouched.
func TestWriteFailureLeavesNoPart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx.gob")
	if err := os.WriteFile(path, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, testHeader{"MAGIC", 3}, make(chan int)); err == nil {
		t.Fatal("encoding a channel must fail")
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part must be removed after a failed write, stat err = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "previous" {
		t.Fatalf("existing file changed to %q", b)
	}
	if err := Write(filepath.Join(t.TempDir(), "missing", "idx.gob"), testHeader{}, 1); err == nil {
		t.Fatal("a write into a missing directory must fail")
	}
}

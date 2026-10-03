package dataLoader

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// splitTab splits line at every tab and returns the fields, reusing dst's
// backing array. GeoNames files are plain TSV: there is no quoting or
// escaping, so a double quote is an ordinary character (unlike encoding/csv,
// which treats a bare one as a parse error). The fields are substrings of
// line.
func splitTab(line string, dst []string) []string {
	dst = dst[:0]
	for {
		i := strings.IndexByte(line, '\t')
		if i < 0 {
			return append(dst, line)
		}
		dst = append(dst, line[:i])
		line = line[i+1:]
	}
}

// maxLineBytes caps one input line at 1 MiB. Real GeoNames lines are under
// 10 KB, so only a corrupt or non-text file reaches it; such a line is
// skipped and counted instead of failing the load the way bufio.Scanner's
// ErrTooLong did.
const maxLineBytes = 1 << 20

var byteOrderMark = []byte{0xEF, 0xBB, 0xBF}

// forEachLine calls fn with each line of r and its 1-based number, until fn
// returns false or r is exhausted. It owns the input quirks shared by every
// loader: a UTF-8 BOM before the first line is dropped (it would otherwise
// become part of the first key), a trailing "\r" is trimmed (CRLF files), and
// a line past maxLineBytes is recorded in skipped and not passed on. The
// slice handed to fn is only valid until fn returns.
func forEachLine(r io.Reader, skipped *skipReport, fn func(n int, line []byte) bool) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var long []byte // accumulates a line longer than the reader's buffer
	for n := 1; ; n++ {
		line, err := br.ReadSlice('\n')
		tooLong := false
		for errors.Is(err, bufio.ErrBufferFull) {
			if len(long)+len(line) <= maxLineBytes {
				long = append(long, line...)
			} else {
				tooLong = true
			}
			line, err = br.ReadSlice('\n')
		}
		if len(long) > 0 {
			if !tooLong && len(long)+len(line) <= maxLineBytes {
				line = append(long, line...)
			} else {
				tooLong = true
			}
			long = long[:0]
		}
		if err != nil && err != io.EOF {
			return err
		}
		if err == io.EOF && len(line) == 0 && !tooLong {
			return nil
		}
		if tooLong {
			skipped.add(reasonLineTooLong, n)
		} else {
			if n == 1 {
				line = bytes.TrimPrefix(line, byteOrderMark)
			}
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if !fn(n, line) {
				return nil
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

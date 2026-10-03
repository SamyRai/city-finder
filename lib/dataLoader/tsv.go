package dataLoader

import "strings"

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

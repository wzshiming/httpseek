package httpseek

import (
	"strconv"
	"strings"
)

// parseSingleRange parses a Range header value that contains exactly one byte range
// of the form "bytes=<start>-", "bytes=<start>-<end>", or "bytes=-<suffixLength>".
// Returns start, end (inclusive; -1 if open-ended), and whether parsing succeeded.
// For suffix ranges ("bytes=-N"), start is returned as -1; the actual start offset
// is resolved from the server's Content-Range response header.
func parseSingleRange(rangeHeader string) (start, end int64, ok bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(rangeHeader, prefix) {
		return 0, 0, false
	}
	s := rangeHeader[len(prefix):]

	// suffix range: bytes=-N (last N bytes)
	if len(s) > 0 && s[0] == '-' {
		n, err := strconv.ParseInt(s[1:], 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		return -1, n, true
	}

	// regular range: bytes=<start>- or bytes=<start>-<end>
	dash := strings.IndexByte(s, '-')
	if dash < 0 || dash == 0 {
		return 0, 0, false
	}
	startVal, err := strconv.ParseInt(s[:dash], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	endStr := s[dash+1:]
	if endStr == "" {
		return startVal, -1, true
	}
	endVal, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return startVal, endVal, true
}

// parseContentRange parses a Content-Range header "bytes <start>-<end>/<total>".
// Returns start, end (inclusive), total (-1 if "*"), and whether parsing succeeded.
func parseContentRange(contentRange string) (start, end, total int64, ok bool) {
	// expect "bytes <start>-<end>/<total|*>"
	const prefix = "bytes "
	if !strings.HasPrefix(contentRange, prefix) {
		return 0, 0, 0, false
	}
	s := contentRange[len(prefix):]

	dash := strings.IndexByte(s, '-')
	if dash <= 0 {
		return 0, 0, 0, false
	}
	startVal, err := strconv.ParseInt(s[:dash], 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	s = s[dash+1:]

	slash := strings.IndexByte(s, '/')
	if slash <= 0 {
		return 0, 0, 0, false
	}
	endVal, err := strconv.ParseInt(s[:slash], 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	totalStr := s[slash+1:]

	if totalStr == "*" {
		return startVal, endVal, -1, true
	}
	totalVal, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	return startVal, endVal, totalVal, true
}

// parseUnsatisfiedContentRange parses a 416 Content-Range header "bytes */<total>".
func parseUnsatisfiedContentRange(contentRange string) (total int64, ok bool) {
	const prefix = "bytes */"
	if !strings.HasPrefix(contentRange, prefix) {
		return 0, false
	}
	totalVal, err := strconv.ParseInt(contentRange[len(prefix):], 10, 64)
	if err != nil || totalVal < 0 {
		return 0, false
	}
	return totalVal, true
}

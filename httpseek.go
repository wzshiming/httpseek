package httpseek

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
)

var (
	contentRangeRegexp = regexp.MustCompile(`bytes ([0-9]+)-([0-9]+)/([0-9]+|\\*)`)

	// ErrCodeForByteRange is returned when the HTTP status code is not 206 for a byte range request.
	ErrCodeForByteRange = errors.New("expected HTTP 206 from byte range request")
)

// NewSeeker handles reading from an HTTP endpoint using a GET request.
func NewSeeker(ctx context.Context, transport http.RoundTripper, req *http.Request) *Seeker {
	return &Seeker{
		ctx:       ctx,
		transport: transport,
		req:       req,
	}
}

type Seeker struct {
	ctx       context.Context
	transport http.RoundTripper
	req       *http.Request
	resp      *http.Response

	rc     io.ReadCloser
	offset int64
	size   int64
}

func (s *Seeker) Read(p []byte) (n int, err error) {
	if s.rc == nil {
		err = s.seek(s.offset)
		if err != nil {
			return 0, err
		}
	}

	n, err = s.rc.Read(p)
	s.offset += int64(n)
	return n, err
}

func (s *Seeker) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = s.offset + offset
	case io.SeekEnd:
		if s.size <= 0 {
			// TODO: make a HEAD request to get the content length
			return 0, errors.New("content length not known")
		}
		newOffset = s.size + offset
	}

	return newOffset, s.seek(newOffset)
}

func (s *Seeker) seek(offset int64) error {
	r, size, resp, err := reader(s.transport, s.req.Clone(s.ctx), offset)
	if err != nil {
		return err
	}
	s.reset()
	if offset == 0 {
		s.resp = resp
	}
	s.size = size
	s.offset = offset
	s.rc = r
	return err
}

func (s *Seeker) Close() error {
	s.reset()
	return nil
}

func (s *Seeker) Response() (*http.Response, bool) {
	return s.resp, s.size > 0
}

func (s *Seeker) reset() {
	if s.rc == nil {
		return
	}
	_ = s.rc.Close()
	s.rc = nil
}

func reader(transport http.RoundTripper, req *http.Request, readerOffset int64) (io.ReadCloser, int64, *http.Response, error) {
	if readerOffset > 0 {
		req.Header.Add("Range", fmt.Sprintf("bytes=%d-", readerOffset))
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, 0, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.Body, -1, resp, nil
	}

	if resp.StatusCode == http.StatusOK {
		return resp.Body, resp.ContentLength, resp, nil
	}

	if readerOffset > 0 {
		if resp.StatusCode != http.StatusPartialContent {
			return nil, 0, nil, ErrCodeForByteRange
		}

		contentRange := resp.Header.Get("Content-Range")
		if contentRange == "" {
			return nil, 0, nil, errors.New("no Content-Range header found in HTTP 206 response")
		}

		s, err := getContentLength(contentRange, uint64(readerOffset))
		if err != nil {
			return nil, 0, nil, err
		}
		return resp.Body, s, nil, nil
	}

	return resp.Body, -1, resp, nil
}

func getContentLength(contentRange string, readerOffset uint64) (int64, error) {
	submatches := contentRangeRegexp.FindStringSubmatch(contentRange)
	if len(submatches) < 4 {
		return 0, fmt.Errorf("could not parse Content-Range header: %s", contentRange)
	}

	startByte, err := strconv.ParseUint(submatches[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("could not parse start of range in Content-Range header: %s", contentRange)
	}

	if startByte != readerOffset {
		return 0, fmt.Errorf("received Content-Range starting at offset %d instead of requested %d", startByte, readerOffset)
	}

	endByte, err := strconv.ParseUint(submatches[2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("could not parse end of range in Content-Range header: %s", contentRange)
	}

	if submatches[3] == "*" {
		return -1, nil
	}

	size, err := strconv.ParseUint(submatches[3], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("could not parse total size in Content-Range header: %s", contentRange)
	}

	if endByte+1 != size {
		return 0, fmt.Errorf("range in Content-Range stops before the end of the content: %s", contentRange)
	}

	if size > math.MaxInt64 {
		return 0, fmt.Errorf("Content-Range size: %d exceeds max allowed size", size)
	}
	return int64(size), nil
}

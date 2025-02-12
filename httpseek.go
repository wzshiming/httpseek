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
	rangeKey           = "Range"
	contentRangeKey    = "Content-Range"
	contentRangeRegexp = regexp.MustCompile(`bytes ([0-9]+)-([0-9]+)/([0-9]+|\\*)`)

	// ErrCodeForByteRange is returned when the HTTP status code is not 206 for a byte range request.
	ErrCodeForByteRange = errors.New("expected HTTP 206 from byte range request")

	// ErrNoContentRange is returned when the Content-Range header is missing from a 206 response.
	ErrNoContentRange = errors.New("no Content-Range header found in HTTP 206 response")
)

var (
	_ io.Seeker = (*Seeker)(nil)
	_ io.Reader = (*Seeker)(nil)
	_ io.Closer = (*Seeker)(nil)
)

// NewSeeker handles reading from an HTTP endpoint using a GET request.
func NewSeeker(ctx context.Context, transport http.RoundTripper, req *http.Request) *Seeker {
	return &Seeker{
		ctx:       ctx,
		transport: transport,
		req:       req,
		size:      -1,
	}
}

type Seeker struct {
	ctx           context.Context
	transport     http.RoundTripper
	req           *http.Request
	firstResponse *http.Response

	rc     io.ReadCloser
	offset uint64
	size   int64
}

func (s *Seeker) Read(p []byte) (n int, err error) {
	if s.rc == nil {
		err = s.seek(s.ctx, s.offset)
		if err != nil {
			return 0, err
		}
	}

	n, err = s.rc.Read(p)
	s.offset += uint64(n)
	if err != nil && int64(s.offset) < s.size {
		_ = s.reset()
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
	}
	return n, err
}

// Seek sets the offset for the next Read to offset.
func (s *Seeker) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = int64(s.offset) + offset
	case io.SeekEnd:
		if s.size <= 0 {
			// TODO: make a HEAD request to get the content length
			return 0, errors.New("content length not known")
		}
		newOffset = s.size + offset
	}
	if newOffset < 0 {
		return 0, errors.New("negative offset")
	}

	return newOffset, s.seek(s.ctx, uint64(newOffset))
}

func (s *Seeker) seek(ctx context.Context, offset uint64) error {
	r, size, resp, err := reader(ctx, s.transport, s.req, offset, s.size)
	if err != nil {
		return err
	}
	_ = s.reset()
	if offset == 0 {
		s.firstResponse = resp
	}
	s.size = size
	s.offset = offset
	s.rc = r
	return nil
}

// Close closes the Seeker.
func (s *Seeker) Close() error {
	return s.reset()
}

// Response returns the first HTTP response received from the server.
func (s *Seeker) Response() (*http.Response, error) {
	if s.firstResponse == nil {
		err := s.seek(s.ctx, 0)
		if err != nil {
			return nil, err
		}
	}
	return s.firstResponse, nil
}

// Size returns the content length of the HTTP response.
func (s *Seeker) Size() int64 {
	return s.size
}

// Offset returns the current offset of the Seeker.
func (s *Seeker) Offset() uint64 {
	return s.offset
}

func (s *Seeker) reset() error {
	if s.rc == nil {
		return nil
	}
	err := s.rc.Close()
	s.rc = nil
	return err
}

func reader(ctx context.Context, transport http.RoundTripper, req *http.Request, readerOffset uint64, readerSize int64) (io.ReadCloser, int64, *http.Response, error) {
	req = req.Clone(ctx)
	if readerOffset > 0 {
		req.Header.Add(rangeKey, fmt.Sprintf("bytes=%d-", readerOffset))
	}

	var resp *http.Response
	var err error
	for i := 0; i < 10; i++ {
		resp, err = transport.RoundTrip(req)
		if err != nil {
			return nil, -1, nil, err
		}

		switch resp.StatusCode {
		case http.StatusOK, http.StatusNoContent:
			if readerOffset == 0 {
				return resp.Body, resp.ContentLength, resp, nil
			}
			return nil, -1, nil, ErrCodeForByteRange
		case http.StatusPartialContent:
			contentRange := resp.Header.Get(contentRangeKey)
			if contentRange == "" {
				return nil, -1, nil, ErrNoContentRange
			}

			s, err := getContentLength(contentRange, readerOffset, readerSize)
			if err != nil {
				return nil, -1, nil, err
			}
			return resp.Body, s, nil, nil
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			location := resp.Header.Get("Location")
			if location == "" {
				return resp.Body, -1, resp, nil
			}
			u, err := req.URL.Parse(location)
			if err != nil {
				return resp.Body, -1, resp, nil
			}
			newReq, err := http.NewRequestWithContext(ctx, req.Method, u.String(), nil)
			if err != nil {
				return resp.Body, -1, resp, nil
			}
			newReq.Header = req.Header
			req = newReq
			continue
		default:
			return resp.Body, -1, resp, nil
		}
	}
	return resp.Body, -1, resp, nil
}

func getContentLength(contentRange string, readerOffset uint64, readerSize int64) (int64, error) {
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

	if readerSize > 0 && size != uint64(readerSize) {
		return 0, fmt.Errorf("Content-Range size: %d does not match expected size: %d", size, readerSize)
	}

	if size > math.MaxInt64 {
		return 0, fmt.Errorf("Content-Range size: %d exceeds max allowed size", size)
	}
	return int64(size), nil
}

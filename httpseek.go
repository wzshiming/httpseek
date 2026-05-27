package httpseek

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	rangeKey        = "Range"
	contentRangeKey = "Content-Range"
)

var (
	// ErrCodeForByteRange is returned when the HTTP status code is not 206 for a byte range request.
	ErrCodeForByteRange = errors.New("expected HTTP 206 from byte range request")

	// ErrNoContentRange is returned when the Content-Range header is missing from a 206 response.
	ErrNoContentRange = errors.New("no Content-Range header found in HTTP 206 response")

	// ErrUnsupported indicates that the target response is not supported, such as for 30x or 401 responses.
	ErrUnsupported = errors.New("unsupported target response")
)

var (
	_ io.Seeker = (*Seeker)(nil)
	_ io.Reader = (*Seeker)(nil)
	_ io.Closer = (*Seeker)(nil)
)

// NewSeeker creates a new Seeker for reading from an HTTP endpoint using a GET request.
func NewSeeker(ctx context.Context, transport http.RoundTripper, req *http.Request) *Seeker {
	return NewRangeSeeker(ctx, transport, req, 0, -1)
}

// NewSuffixSeeker creates a Seeker that reads the last suffixLen bytes of an HTTP endpoint.
// The actual byte range is resolved from the server's Content-Range response.
func NewSuffixSeeker(ctx context.Context, transport http.RoundTripper, req *http.Request, suffixLen int64) *Seeker {
	return NewRangeSeeker(ctx, transport, req, -1, suffixLen)
}

// NewRangeSeeker creates a Seeker that reads a bounded byte range [start, end] from an HTTP endpoint.
// end is the inclusive end byte; -1 means open-ended (read to EOF).
func NewRangeSeeker(ctx context.Context, transport http.RoundTripper, req *http.Request, start, end int64) *Seeker {
	return &Seeker{
		ctx:       ctx,
		transport: transport,
		req:       req,
		offset:    start,
		end:       end,
	}
}

type HTTPClient interface {
	Do(r *http.Request) (*http.Response, error)
}

// NewSeekerWithHTTPClient creates a Seeker that includes HTTP client capabilities to handle redirects.
func NewSeekerWithHTTPClient(ctx context.Context, httpClient HTTPClient, req *http.Request) *Seeker {
	return NewSeeker(ctx, httpClientToRoundTripper{httpClient}, req)
}

type Seeker struct {
	ctx           context.Context
	transport     http.RoundTripper
	req           *http.Request
	firstResponse *http.Response

	rc     io.ReadCloser
	offset int64
	size   int64
	end    int64 // inclusive end byte for range reads; -1 for open-ended
}

func (s *Seeker) Read(p []byte) (n int, err error) {
	if s.rc == nil {
		err = s.seek(s.ctx, s.offset)
		if err != nil {
			return 0, err
		}
	}

	if s.rc == nil {
		return 0, ErrUnsupported
	}

	if s.end >= 0 {
		// If the end byte is known, limit the read to the remaining bytes in the range.
		remaining := s.end - s.offset + 1
		if remaining <= 0 {
			return 0, io.EOF
		}
		if int64(len(p)) > remaining {
			p = p[:remaining]
		}
	}

	n, err = s.rc.Read(p)
	s.offset += int64(n)
	// For range reads, determine the effective end; for whole-file reads, use total size.
	atEnd := false
	if s.end >= 0 {
		atEnd = s.offset > s.end
	} else {
		atEnd = s.size > 0 && s.offset >= s.size
	}
	if err != nil && !atEnd {
		_ = s.reset()
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
	}
	return n, err
}

// Seek sets the offset for the next Read to the specified offset.
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

	if s.offset != newOffset {
		_ = s.reset()
		s.offset = newOffset
	}
	return newOffset, nil
}

func (s *Seeker) seek(ctx context.Context, offset int64) error {
	r, size, resolvedOffset, resolvedEnd, resp, err := reader(ctx, s.transport, s.req, offset, s.end)
	if err != nil {
		return err
	}
	_ = s.reset()
	if s.firstResponse == nil && resp != nil {
		s.firstResponse = resp
	}
	s.size = size
	s.rc = r
	s.offset = resolvedOffset
	s.end = resolvedEnd
	return nil
}

// Close closes the Seeker.
func (s *Seeker) Close() error {
	return s.reset()
}

// OK indicates whether the Seeker is ready to be read.
func (s *Seeker) OK() bool {
	return s.rc != nil
}

// Response returns the first HTTP response received from the server.
func (s *Seeker) Response() (*http.Response, error) {
	if s.firstResponse == nil {
		err := s.seek(s.ctx, s.offset)
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
func (s *Seeker) Offset() int64 {
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

func reader(ctx context.Context, transport http.RoundTripper, req *http.Request, readerOffset int64, readerEnd int64) (io.ReadCloser, int64, int64, int64, *http.Response, error) {
	req = req.Clone(ctx)

	switch {
	case readerOffset < 0: // suffix range; readerEnd is the suffix length (positive)
		req.Header.Set(rangeKey, fmt.Sprintf("bytes=-%d", readerEnd))
	case readerOffset >= 0 && readerEnd >= 0:
		req.Header.Set(rangeKey, fmt.Sprintf("bytes=%d-%d", readerOffset, readerEnd))
	case readerOffset > 0: // open-ended range from a non-zero offset
		req.Header.Set(rangeKey, fmt.Sprintf("bytes=%d-", readerOffset))
	default:
		// readerOffset == 0 && readerEnd < 0: plain full-file GET, no Range header needed
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, -1, readerOffset, readerEnd, nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		if readerOffset == 0 {
			return resp.Body, resp.ContentLength, readerOffset, readerEnd, resp, nil
		}
		resp.Body.Close()
		return nil, -1, readerOffset, readerEnd, nil, ErrCodeForByteRange
	case http.StatusPartialContent:
		contentRange := resp.Header.Get(contentRangeKey)
		if contentRange == "" {
			resp.Body.Close()
			return nil, -1, readerOffset, readerEnd, nil, ErrNoContentRange
		}

		actualStart, actualEnd, total, ok := parseContentRange(contentRange)
		if !ok {
			resp.Body.Close()
			return nil, -1, readerOffset, readerEnd, nil, fmt.Errorf("could not parse Content-Range header: %s", contentRange)
		}
		if readerOffset >= 0 && actualStart != readerOffset {
			resp.Body.Close()
			return nil, -1, readerOffset, readerEnd, nil, fmt.Errorf("unexpected Content-Range start: got %d, want %d", actualStart, readerOffset)
		}
		if readerOffset >= 0 && readerEnd >= 0 && actualEnd > readerEnd {
			resp.Body.Close()
			return nil, -1, readerOffset, readerEnd, nil, fmt.Errorf("unexpected Content-Range end: got %d, want <= %d", actualEnd, readerEnd)
		}
		return resp.Body, total, actualStart, actualEnd, resp, nil
	}

	resp.Body.Close()
	return nil, -1, readerOffset, readerEnd, resp, nil
}

type httpClientToRoundTripper struct {
	client HTTPClient
}

func (h httpClientToRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return h.client.Do(r)
}

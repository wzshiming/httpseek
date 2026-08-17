package httpseek

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	rangeKey        = "Range"
	contentRangeKey = "Content-Range"
	ifRangeKey      = "If-Range"
	etagKey         = "ETag"
	lastModifiedKey = "Last-Modified"

	// maxSkipBytes is the largest forward-seek distance served by discarding
	// bytes from the current connection instead of issuing a new request.
	maxSkipBytes = 256 << 10
)

var (
	// ErrCodeForByteRange is returned when the HTTP status code is not 206 for a byte range request.
	ErrCodeForByteRange = errors.New("expected HTTP 206 from byte range request")

	// ErrNoContentRange is returned when the Content-Range header is missing from a 206 response.
	ErrNoContentRange = errors.New("no Content-Range header found in HTTP 206 response")

	// ErrInvalidContentRange is returned when the Content-Range header cannot be parsed or does not match the requested range.
	ErrInvalidContentRange = errors.New("invalid Content-Range header")

	// ErrRangeNotSatisfiable is returned when the server responds with HTTP 416.
	ErrRangeNotSatisfiable = errors.New("requested range not satisfiable")

	// ErrContentChanged is returned when the content changed between requests:
	// a response validator mismatched, or a server honoring If-Range answered a
	// range request with a full response. Subsequent reads keep failing; create
	// a new Seeker to read the changed content.
	ErrContentChanged = errors.New("content changed during read")

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

// Seeker reads an HTTP resource via Range requests, implementing io.ReadSeekCloser.
// It is not safe for concurrent use.
type Seeker struct {
	ctx           context.Context
	transport     http.RoundTripper
	req           *http.Request
	firstResponse *http.Response

	rc        io.ReadCloser
	offset    int64
	size      int64
	sizeKnown bool
	end       int64  // inclusive end byte for range reads; -1 for open-ended
	validator string // strong ETag or Last-Modified sent as If-Range on re-requests
	err       error  // classification of the last unsupported response
}

func (s *Seeker) Read(p []byte) (n int, err error) {
	// Already at the end of the requested range or known file size.
	if s.offset >= 0 {
		if s.end >= 0 {
			if s.offset > s.end {
				return 0, io.EOF
			}
		} else if s.sizeKnown && s.offset >= s.size {
			return 0, io.EOF
		}
	}

	if s.rc == nil {
		if s.err != nil {
			return 0, s.stickyErr()
		}
		err = s.seek(s.ctx, s.offset)
		if err != nil {
			return 0, err
		}
		if s.rc == nil {
			if err = s.stickyErr(); err != nil {
				return 0, err
			}
			return 0, ErrUnsupported
		}
	}

	if s.end >= 0 {
		// If the end byte is known, limit the read to the remaining bytes in the range.
		if remaining := s.end - s.offset; len(p) > 0 && int64(len(p)-1) > remaining {
			p = p[:remaining+1]
		}
	}

	n, err = s.rc.Read(p)
	s.offset += int64(n)
	if err != nil {
		// For range reads, determine the effective end; for whole-file reads, use total size.
		atEnd := false
		switch {
		case s.end >= 0:
			atEnd = s.offset > s.end || s.sizeKnown && s.offset >= s.size
		case s.sizeKnown:
			atEnd = s.offset >= s.size
		default:
			// Total size unknown: trust the server's EOF.
			atEnd = err == io.EOF
		}
		if !atEnd {
			_ = s.reset()
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
		}
	}
	return n, err
}

// stickyErr reports the cached error from the last unsupported response.
// A range beyond EOF reads as io.EOF, matching file semantics.
func (s *Seeker) stickyErr() error {
	if errors.Is(s.err, ErrRangeNotSatisfiable) && s.offset >= 0 {
		return io.EOF
	}
	return s.err
}

// Seek sets the offset for the next Read to the specified offset.
// Seeking to a different offset clears the error classification cached from
// the last unsupported response; seeking to the current offset preserves it.
func (s *Seeker) Seek(offset int64, whence int) (int64, error) {
	if s.offset < 0 && whence != io.SeekStart {
		// Unresolved suffix range: relative seeks need the actual offset from the server.
		if s.err == nil {
			if err := s.seek(s.ctx, s.offset); err != nil {
				return 0, err
			}
		}
		if s.offset < 0 {
			// Still unresolved (e.g. 416 on an empty file): only an absolute seek escapes.
			if s.err != nil {
				return 0, s.err
			}
			return 0, ErrUnsupported
		}
	}

	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = s.offset + offset
	case io.SeekEnd:
		if !s.sizeKnown {
			// Probe the server to learn the total size.
			if err := s.seek(s.ctx, s.offset); err != nil {
				return 0, err
			}
			if !s.sizeKnown {
				if s.err != nil {
					return 0, s.err
				}
				return 0, errors.New("content length not known")
			}
		}
		newOffset = s.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if newOffset < 0 {
		return 0, errors.New("negative offset")
	}

	if s.offset < 0 {
		// Absolute seek escapes an unresolved suffix range; its end is a length, not a position.
		s.end = -1
	}
	if s.offset != newOffset {
		// Serve short forward seeks by discarding from the current connection.
		if s.rc != nil && newOffset > s.offset && newOffset-s.offset <= maxSkipBytes && (s.end < 0 || newOffset-1 <= s.end) {
			if _, err := io.CopyN(io.Discard, s.rc, newOffset-s.offset); err == nil {
				s.offset = newOffset
				return newOffset, nil
			}
			// Discard failed; fall back to a fresh request.
		}
		_ = s.reset()
		s.offset = newOffset
		s.err = nil
	}
	return newOffset, nil
}

func (s *Seeker) seek(ctx context.Context, offset int64) error {
	r, size, resolvedOffset, resolvedEnd, resp, err := reader(ctx, s.transport, s.req, offset, s.end, s.validator)
	if err != nil {
		return err
	}
	_ = s.reset()
	if s.firstResponse == nil && resp != nil {
		s.firstResponse = resp
	}
	s.err = nil
	if r == nil && resp != nil {
		// Unsupported response: classify it so Read can report a meaningful error.
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			s.err = ErrRangeNotSatisfiable
		} else {
			s.err = fmt.Errorf("%w: %s", ErrUnsupported, resp.Status)
		}
	} else if resp != nil && s.validator == "" {
		// Remember a validator so later requests can detect content changes.
		s.validator = responseValidator(resp.Header)
	}
	if size >= 0 {
		s.size = size
		s.sizeKnown = true
	} else if !s.sizeKnown {
		// Keep a previously known size when the new response does not report one.
		s.size = size
	}
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

// responseValidator picks a validator for If-Range: a strong ETag (always
// quoted), else Last-Modified. Weak or malformed ETags are ignored.
func responseValidator(h http.Header) string {
	if etag := h.Get(etagKey); strings.HasPrefix(etag, `"`) {
		return etag
	}
	return h.Get(lastModifiedKey)
}

// matchingValidator reports whether the response carries a validator of the
// same kind as the stored one with an identical value.
func matchingValidator(validator string, h http.Header) bool {
	if strings.HasPrefix(validator, `"`) {
		return h.Get(etagKey) == validator
	}
	return h.Get(lastModifiedKey) == validator
}

// conflictingValidator returns the response's validator of the same kind as
// the stored one when it differs; a response lacking that kind is inconclusive.
func conflictingValidator(validator string, h http.Header) (string, bool) {
	if strings.HasPrefix(validator, `"`) {
		if etag := h.Get(etagKey); strings.HasPrefix(etag, `"`) && etag != validator {
			return etag, true
		}
		return "", false
	}
	if lm := h.Get(lastModifiedKey); lm != "" && lm != validator {
		return lm, true
	}
	return "", false
}

func reader(ctx context.Context, transport http.RoundTripper, req *http.Request, readerOffset int64, readerEnd int64, validator string) (io.ReadCloser, int64, int64, int64, *http.Response, error) {
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
	if validator != "" && req.Header.Get(rangeKey) != "" {
		// Fail range requests with a non-206 response if the content changed.
		req.Header.Set(ifRangeKey, validator)
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, -1, readerOffset, readerEnd, nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusPartialContent:
		if validator != "" {
			if v, changed := conflictingValidator(validator, resp.Header); changed {
				resp.Body.Close()
				return nil, -1, readerOffset, readerEnd, nil, fmt.Errorf("%w: validator changed from %q to %q", ErrContentChanged, validator, v)
			}
		}
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		if readerOffset == 0 {
			return resp.Body, resp.ContentLength, readerOffset, readerEnd, resp, nil
		}
		resp.Body.Close()
		if validator != "" && !matchingValidator(validator, resp.Header) {
			// A server honoring If-Range ignores Range when the validator no longer matches.
			return nil, -1, readerOffset, readerEnd, nil, fmt.Errorf("%w: full response to a range request with If-Range", ErrContentChanged)
		}
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
			return nil, -1, readerOffset, readerEnd, nil, fmt.Errorf("%w: could not parse %q", ErrInvalidContentRange, contentRange)
		}
		if readerOffset >= 0 && actualStart != readerOffset {
			resp.Body.Close()
			return nil, -1, readerOffset, readerEnd, nil, fmt.Errorf("%w: unexpected start: got %d, want %d", ErrInvalidContentRange, actualStart, readerOffset)
		}
		if readerOffset >= 0 && readerEnd >= 0 && actualEnd > readerEnd {
			resp.Body.Close()
			return nil, -1, readerOffset, readerEnd, nil, fmt.Errorf("%w: unexpected end: got %d, want <= %d", ErrInvalidContentRange, actualEnd, readerEnd)
		}
		return resp.Body, total, actualStart, actualEnd, resp, nil
	case http.StatusRequestedRangeNotSatisfiable:
		// The requested range is beyond EOF; learn the total size if reported.
		discardResponseBody(resp)
		size := int64(-1)
		if total, ok := parseUnsatisfiedContentRange(resp.Header.Get(contentRangeKey)); ok {
			size = total
		}
		return nil, size, readerOffset, readerEnd, resp, nil
	}

	discardResponseBody(resp)
	return nil, -1, readerOffset, readerEnd, resp, nil
}

func discardResponseBody(resp *http.Response) {
	resp.Body.Close()
	resp.Body = http.NoBody
	resp.ContentLength = 0
	resp.Header.Del("Content-Length")
}

type httpClientToRoundTripper struct {
	client HTTPClient
}

func (h httpClientToRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return h.client.Do(r)
}

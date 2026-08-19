package httpseek

import (
	"context"
	"errors"
	"io"
	"net/http"
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

	// ErrSizeUnknown is returned by Seek with io.SeekEnd when the total size
	// of the resource cannot be determined.
	ErrSizeUnknown = errors.New("resource size unknown")
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
// A negative start requests the suffix of the last end bytes.
func NewRangeSeeker(ctx context.Context, transport http.RoundTripper, req *http.Request, start, end int64) *Seeker {
	opener := &httpOpener{
		ctx:       ctx,
		transport: transport,
		req:       req,
	}
	return &Seeker{
		OpenSeeker: NewRangeOpenSeeker(opener, start, end),
		opener:     opener,
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
	*OpenSeeker
	opener *httpOpener
}

// Response returns the first HTTP response received from the server.
func (s *Seeker) Response() (*http.Response, error) {
	if s.opener.firstResponse == nil {
		if err := s.open(); err != nil {
			return nil, err
		}
	}
	return s.opener.firstResponse, nil
}

type httpClientToRoundTripper struct {
	client HTTPClient
}

func (h httpClientToRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return h.client.Do(r)
}

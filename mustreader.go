package httpseek

import (
	"errors"
	"io"
	"net/http"
)

type mustReader struct {
	rsc          io.ReadSeeker
	errorHandler func(error) error
	offset       int
}

// NewMustReader returns a reader that will retry reading with partial byte ranges if the underlying reader returns an error.
func NewMustReader(rsc io.ReadSeeker, errorHandler func(error) error) io.Reader {
	return &mustReader{
		rsc:          rsc,
		errorHandler: errorHandler,
	}
}

func (r *mustReader) Read(p []byte) (n int, err error) {
	return r.read(p)
}

func (r *mustReader) read(p []byte) (n int, err error) {
	n, err = r.rsc.Read(p)
	r.offset += n
	if err == nil {
		return n, nil
	}

	if err == io.EOF {
		return n, err
	}

	if r.errorHandler != nil {
		if err = r.errorHandler(err); err != nil {
			return n, err
		}
	}

	for {
		_, err = r.rsc.Seek(int64(r.offset), io.SeekStart)
		if err == nil {
			return r.read(p)
		}

		if errors.Is(err, ErrCodeForByteRange) {
			return 0, err
		}

		if r.errorHandler != nil {
			if err = r.errorHandler(err); err != nil {
				return n, err
			}
		}
	}
}

type mustReaderTransport struct {
	baseTransport http.RoundTripper
	errorHandler  func(error) error
}

// NewMustReaderTransport returns a transport that will retry reading with partial byte ranges if the underlying transport returns an error.
func NewMustReaderTransport(baseTransport http.RoundTripper, errorHandler func(error) error) http.RoundTripper {
	return &mustReaderTransport{
		baseTransport: baseTransport,
		errorHandler:  errorHandler,
	}
}

func (t *mustReaderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return t.baseTransport.RoundTrip(r)
	}

	var err error
	rsc := NewSeeker(r.Context(), t.baseTransport, r)
	for {
		_, err = rsc.Seek(0, io.SeekStart)
		if err == nil {
			break
		}
		if t.errorHandler != nil {
			if err = t.errorHandler(err); err != nil {
				return nil, err
			}
		}
	}

	mr := NewMustReader(rsc, t.errorHandler)

	resp := *rsc.Response()
	resp.Body = struct {
		io.Reader
		io.Closer
	}{
		Reader: mr,
		Closer: rsc,
	}

	return &resp, nil
}

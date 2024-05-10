package httpseek

import (
	"net/http"
)

type mustReaderTransport struct {
	baseTransport http.RoundTripper
	errorHandler  func(*http.Request, int, error) error
}

// NewMustReaderTransport returns a transport that will retry reading with partial byte ranges if the underlying transport returns an error.
func NewMustReaderTransport(baseTransport http.RoundTripper, errorHandler func(*http.Request, int, error) error) http.RoundTripper {
	return &mustReaderTransport{
		baseTransport: baseTransport,
		errorHandler:  errorHandler,
	}
}

// RoundTrip executes a single HTTP transaction.
func (t *mustReaderTransport) RoundTrip(r *http.Request) (resp *http.Response, err error) {
	if r.Method != http.MethodGet {
		return t.baseTransport.RoundTrip(r)
	}

	var retry = 0
	rsc := NewSeeker(r.Context(), t.baseTransport, r)
	for {
		resp, err = rsc.Response()
		if err == nil {
			break
		}
		if t.errorHandler != nil {
			if err = t.errorHandler(r, retry, err); err != nil {
				return nil, err
			}
			retry++
		}
	}

	size := rsc.Size()
	if size <= 0 {
		return resp, nil
	}

	var readerErrorHandler func(retry int, err error) error
	if t.errorHandler != nil {
		readerErrorHandler = func(retry0 int, err error) error {
			return t.errorHandler(r, retry+retry0, err)
		}
	}

	resp.Body = NewMustReadCloser(rsc, readerErrorHandler)
	return resp, nil
}

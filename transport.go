package httpseek

import (
	"net/http"
)

type mustReaderTransport struct {
	baseTransport http.RoundTripper
	errorHandler  func(*http.Request, error) error
}

// NewMustReaderTransport returns a transport that will retry reading with partial byte ranges if the underlying transport returns an error.
func NewMustReaderTransport(baseTransport http.RoundTripper, errorHandler func(*http.Request, error) error) http.RoundTripper {
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

	rsc := NewSeeker(r.Context(), t.baseTransport, r)
	for {
		resp, err = rsc.Response()
		if err == nil {
			break
		}
		if t.errorHandler != nil {
			if err = t.errorHandler(r, err); err != nil {
				return nil, err
			}
		}
	}

	size := rsc.Size()
	if size <= 0 {
		return resp, nil
	}

	var readerErrorHandler func(err error) error
	if t.errorHandler != nil {
		readerErrorHandler = func(err error) error {
			return t.errorHandler(r, err)
		}
	}

	resp.Body = NewMustReadCloser(rsc, readerErrorHandler)
	return resp, nil
}

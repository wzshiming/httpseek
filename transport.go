package httpseek

import (
	"errors"
	"net/http"
	"time"
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

// RetryWithBackoff returns an error handler for NewMustReaderTransport that retries
// up to maxRetries times with exponential backoff starting at base (no sleep if base <= 0).
func RetryWithBackoff(maxRetries int, base time.Duration) func(*http.Request, int, error) error {
	const maxBackoff = 30 * time.Second
	return func(req *http.Request, retry int, err error) error {
		ctx := req.Context()
		if contextErr := ctx.Err(); contextErr != nil {
			return errors.Join(err, contextErr)
		}
		if retry >= maxRetries {
			return err
		}
		if base <= 0 {
			return nil
		}
		d := base << uint(retry)
		if d <= 0 || d > maxBackoff {
			d = maxBackoff
		}
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-t.C:
			return nil
		}
	}
}

func (t *mustReaderTransport) roundTrip(retry int, r *http.Request) (resp *http.Response, err error) {
	for {
		resp, err = t.baseTransport.RoundTrip(r)
		if err == nil {
			return resp, nil
		}
		if t.errorHandler == nil {
			return nil, err
		}
		if err = t.errorHandler(r, retry, err); err != nil {
			return nil, err
		}
		retry++
	}
}

// RoundTrip executes a single HTTP transaction.
func (t *mustReaderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.Method {
	case http.MethodHead, http.MethodOptions:
		return t.roundTrip(0, r)
	case http.MethodGet:
		// For GET requests, we need to handle potential retries with byte ranges if the server supports it.
	default:
		return t.baseTransport.RoundTrip(r)
	}

	if rangeHeader := r.Header.Get(rangeKey); rangeHeader != "" {
		start, end, ok := parseSingleRange(rangeHeader)
		if !ok {
			return t.roundTrip(0, r)
		}
		if start < 0 {
			return t.roundTripWithSeeker(r, NewSuffixSeeker(r.Context(), t.baseTransport, r, end))
		}
		return t.roundTripWithSeeker(r, NewRangeSeeker(r.Context(), t.baseTransport, r, start, end))
	}

	return t.roundTripWithSeeker(r, NewSeeker(r.Context(), t.baseTransport, r))
}

// roundTripWithSeeker performs a retriable GET using an existing Seeker.
func (t *mustReaderTransport) roundTripWithSeeker(r *http.Request, rsc *Seeker) (*http.Response, error) {
	var retry int
	var resp *http.Response
	var err error
	for {
		resp, err = rsc.Response()
		if err == nil {
			break
		}
		if t.errorHandler == nil {
			return nil, err
		}
		if err = t.errorHandler(r, retry, err); err != nil {
			return nil, err
		}
		retry++
	}

	if !rsc.OK() {
		return resp, nil
	}

	if rsc.Size() <= 0 {
		return resp, nil
	}

	var readerErrorHandler func(int, error) error
	if t.errorHandler != nil {
		readerErrorHandler = func(retry0 int, err error) error {
			return t.errorHandler(r, retry+retry0, err)
		}
	}

	resp.Body = NewMustReadSeekCloser(rsc, rsc.Offset(), readerErrorHandler)
	return resp, nil
}

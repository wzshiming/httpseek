package httpseek

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

const (
	rangeKey        = "Range"
	contentRangeKey = "Content-Range"
	ifRangeKey      = "If-Range"
	etagKey         = "ETag"
	lastModifiedKey = "Last-Modified"
)

// httpOpener adapts an HTTP endpoint to the Opener interface on top of reader.
type httpOpener struct {
	ctx           context.Context
	transport     http.RoundTripper
	req           *http.Request
	firstResponse *http.Response
}

var _ Opener = (*httpOpener)(nil)

func (o *httpOpener) OpenRange(validator string, start, end int64) (OpenResult, error) {
	res, resp, err := reader(o.ctx, o.transport, o.req, start, end, validator)
	if o.firstResponse == nil && resp != nil {
		o.firstResponse = resp
	}
	return res, err
}

// Size learns the total size with a HEAD request.
func (o *httpOpener) Size(validator string) (SizeResult, error) {
	req := o.req.Clone(o.ctx)
	req.Method = http.MethodHead
	// The base request may carry a body and range headers; the probe wants
	// neither, only the whole size.
	req.Body, req.GetBody, req.ContentLength = nil, nil, 0
	req.Header.Del(rangeKey)
	req.Header.Del(ifRangeKey)
	resp, err := o.transport.RoundTrip(req)
	if err != nil {
		return SizeResult{Size: -1}, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		// Report the response's validator; the caller detects content changes.
		return SizeResult{Size: resp.ContentLength, Validator: reportedValidator(validator, resp.Header)}, nil
	}
	return SizeResult{Size: -1}, fmt.Errorf("%w: %s", ErrUnsupported, resp.Status)
}

// responseValidator picks the best validator from a response: a strong ETag
// (always quoted), else Last-Modified. Weak or malformed ETags are ignored.
func responseValidator(h http.Header) string {
	if etag := h.Get(etagKey); strings.HasPrefix(etag, `"`) {
		return etag
	}
	return h.Get(lastModifiedKey)
}

// reportedValidator returns the response's validator of the same kind as the
// stored one, so equality means an unchanged version; a response lacking that
// kind is inconclusive and reports "". Without a stored one it picks the best.
func reportedValidator(validator string, h http.Header) string {
	if validator == "" {
		return responseValidator(h)
	}
	if strings.HasPrefix(validator, `"`) {
		if etag := h.Get(etagKey); strings.HasPrefix(etag, `"`) {
			return etag
		}
		return ""
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

// reader issues the range request and folds the response into an OpenResult.
// The returned response, when non-nil, is safe to expose via Seeker.Response.
func reader(ctx context.Context, transport http.RoundTripper, req *http.Request, readerOffset int64, readerEnd int64, validator string) (OpenResult, *http.Response, error) {
	req = req.Clone(ctx)
	res := OpenResult{Size: -1, Start: readerOffset, End: readerEnd}

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
		return res, nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		if readerOffset == 0 {
			res.Body = resp.Body
			res.Size = resp.ContentLength
			res.Validator = reportedValidator(validator, resp.Header)
			return res, resp, nil
		}
		resp.Body.Close()
		if validator != "" && !matchingValidator(validator, resp.Header) {
			// A server honoring If-Range ignores Range when the validator no longer matches.
			return res, nil, fmt.Errorf("%w: full response to a range request with If-Range", ErrContentChanged)
		}
		return res, nil, ErrCodeForByteRange
	case http.StatusPartialContent:
		contentRange := resp.Header.Get(contentRangeKey)
		if contentRange == "" {
			resp.Body.Close()
			return res, nil, ErrNoContentRange
		}

		actualStart, actualEnd, total, ok := parseContentRange(contentRange)
		if !ok {
			resp.Body.Close()
			return res, nil, fmt.Errorf("%w: could not parse %q", ErrInvalidContentRange, contentRange)
		}
		if readerOffset >= 0 && actualStart != readerOffset {
			resp.Body.Close()
			return res, nil, fmt.Errorf("%w: unexpected start: got %d, want %d", ErrInvalidContentRange, actualStart, readerOffset)
		}
		if readerOffset >= 0 && readerEnd >= 0 && actualEnd > readerEnd {
			resp.Body.Close()
			return res, nil, fmt.Errorf("%w: unexpected end: got %d, want <= %d", ErrInvalidContentRange, actualEnd, readerEnd)
		}
		res.Body = resp.Body
		res.Size = total
		res.Start = actualStart
		res.End = actualEnd
		res.Validator = reportedValidator(validator, resp.Header)
		return res, resp, nil
	case http.StatusRequestedRangeNotSatisfiable:
		// The requested range is beyond EOF; learn the total size if reported.
		discardResponseBody(resp)
		if total, ok := parseUnsatisfiedContentRange(resp.Header.Get(contentRangeKey)); ok {
			res.Size = total
		}
		// A 416 still describes the current content version.
		res.Validator = reportedValidator(validator, resp.Header)
		return res, resp, ErrRangeNotSatisfiable
	}

	discardResponseBody(resp)
	// An error response's validators do not describe the resource.
	return res, resp, fmt.Errorf("%w: %s", ErrUnsupported, resp.Status)
}

func discardResponseBody(resp *http.Response) {
	resp.Body.Close()
	resp.Body = http.NoBody
	resp.ContentLength = 0
	resp.Header.Del("Content-Length")
}

package httpseek

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestMustReadTransport(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(&errorResponseWriter{rw: w, n: rand.Intn(2)}, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))

	s.Client().Transport = NewMustReaderTransport(s.Client().Transport, func(r *http.Request, retry int, err error) error {
		// The server yields 0 or 1 byte per response; a run of zero-byte
		// responses within one Read must not exhaust the retry budget.
		if retry >= 100 {
			return err
		}
		t.Log("Retry", "error", err, "times", retry)
		return nil
	})

	resp, err := s.Client().Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "Hello World!" {
		t.Fatalf("got %q, want %q", got, "Hello World!")
	}
}

func TestMustReadTransportWithRange(t *testing.T) {
	content := []byte("Hello World!")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(&errorResponseWriter{rw: w, n: rand.Intn(2)}, r, "test", time.Time{}, bytes.NewReader(content))
	}))

	s.Client().Transport = NewMustReaderTransport(s.Client().Transport, func(r *http.Request, retry int, err error) error {
		if retry >= 100 {
			return err
		}
		t.Log("Retry", "error", err, "times", retry)
		return nil
	})

	tests := []struct {
		rangeHeader string
		want        string
	}{
		{"bytes=6-", "World!"},
		{"bytes=0-4", "Hello"},
		{"bytes=6-10", "World"},
		{"bytes=-6", "World!"},
	}

	for _, tt := range tests {
		t.Run(tt.rangeHeader, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, s.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Range", tt.rangeHeader)

			resp, err := s.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("expected 206, got %d", resp.StatusCode)
			}

			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}

			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMustReadTransport416PassThrough(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))
	defer s.Close()

	s.Client().Transport = NewMustReaderTransport(s.Client().Transport, nil)

	req, err := http.NewRequest(http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=100-200")

	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected 416, got %d", resp.StatusCode)
	}
	if resp.ContentLength != 0 {
		t.Fatalf("got ContentLength %d, want 0", resp.ContentLength)
	}
	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		t.Fatalf("got Content-Length header %q, want none", contentLength)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("pass-through body must be readable: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("got %q, want empty body", body)
	}
}

func TestRetryWithBackoffCanceled(t *testing.T) {
	for _, base := range []time.Duration{0, time.Hour} {
		t.Run(base.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid", nil)
			if err != nil {
				t.Fatal(err)
			}

			requests := 0
			baseTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("temporary error")
			})
			transport := NewMustReaderTransport(baseTransport, RetryWithBackoff(3, base))

			_, err = transport.RoundTrip(req)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v, want context.Canceled", err)
			}
			if requests != 1 {
				t.Fatalf("got %d requests, want 1", requests)
			}
		})
	}
}

func TestRetryWithBackoffLimit(t *testing.T) {
	wantErr := errors.New("temporary error")
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}

	requests := 0
	baseTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, wantErr
	})
	transport := NewMustReaderTransport(baseTransport, RetryWithBackoff(3, 0))

	_, err = transport.RoundTrip(req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("got %v, want %v", err, wantErr)
	}
	if requests != 4 {
		t.Fatalf("got %d requests, want 4", requests)
	}
}

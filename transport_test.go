package httpseek

import (
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMustReadTransport(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(&errorResponseWriter{rw: w, n: rand.Intn(2)}, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))

	s.Client().Transport = NewMustReaderTransport(s.Client().Transport, func(r *http.Request, retry int, err error) error {
		if retry >= 10 {
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
		if retry >= 10 {
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

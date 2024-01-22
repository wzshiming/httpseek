package httpseek

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMustRead(t *testing.T) {
	ctx := context.Background()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(&errorResponseWriter{rw: w, n: rand.Intn(3)}, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	r := NewMustReader(rsc, nil)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "Hello World!" {
		t.Fatalf("got %q, want %q", got, "Hello World!")
	}
}

func TestMustReadTransport(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(&errorResponseWriter{rw: w, n: rand.Intn(3)}, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))

	s.Client().Transport = NewMustReaderTransport(s.Client().Transport, func(err error) error {
		return nil
	})

	resp, err := s.Client().Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "Hello World!" {
		t.Fatalf("got %q, want %q", got, "Hello World!")
	}
}

type errorResponseWriter struct {
	rw http.ResponseWriter
	n  int
}

func (l *errorResponseWriter) Write(p []byte) (n int, err error) {
	if l.n <= 0 {
		return 0, fmt.Errorf("intentional error")
	}

	if len(p) > l.n {
		p = p[:l.n]
	}

	n, err = l.rw.Write(p)
	l.n -= n
	return n, err
}

func (l *errorResponseWriter) Header() http.Header {
	return l.rw.Header()
}

func (l *errorResponseWriter) WriteHeader(statusCode int) {
	l.rw.WriteHeader(statusCode)
}

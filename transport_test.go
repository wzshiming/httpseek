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
		http.ServeContent(&errorResponseWriter{rw: w, n: rand.Intn(3)}, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))

	s.Client().Transport = NewMustReaderTransport(s.Client().Transport, func(r *http.Request, err error) error {
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

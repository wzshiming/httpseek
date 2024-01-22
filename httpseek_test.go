package httpseek

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSeek(t *testing.T) {
	ctx := context.Background()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "Hello World!" {
		t.Fatalf("got %q, want %q", got, "Hello World!")
	}

	offset, err := rsc.Seek(6, io.SeekStart)
	if err != nil {
		t.Fatal(err)
	}

	if offset != 6 {
		t.Fatalf("got %d, want %d", offset, 6)
	}

	got, err = io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
}

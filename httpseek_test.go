package httpseek

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestReadRangeEndingAtMaxInt64(t *testing.T) {
	ctx := context.Background()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2")
		_, _ = w.Write([]byte("xy"))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewRangeSeeker(ctx, s.Client().Transport, req, 0, math.MaxInt64)
	defer rsc.Close()

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "xy" {
		t.Fatalf("got %q, want %q", got, "xy")
	}
}

func TestSuffixSeekerSeekBeforeRead(t *testing.T) {
	ctx := context.Background()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSuffixSeeker(ctx, s.Client().Transport, req, 6)
	defer rsc.Close()

	// Seeking before the first read must resolve the actual suffix offset.
	offset, err := rsc.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 6 {
		t.Fatalf("got offset %d, want 6", offset)
	}

	offset, err = rsc.Seek(7, io.SeekStart)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 7 {
		t.Fatalf("got offset %d, want 7", offset)
	}

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "orld!" {
		t.Fatalf("got %q, want %q", got, "orld!")
	}
}

func TestSuffixSeekerSeekStartSkipsResolve(t *testing.T) {
	ctx := context.Background()

	var requests int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSuffixSeeker(ctx, s.Client().Transport, req, 6)
	defer rsc.Close()

	// An absolute seek does not depend on the suffix position: no resolve request.
	offset, err := rsc.Seek(7, io.SeekStart)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 7 {
		t.Fatalf("got offset %d, want 7", offset)
	}

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "orld!" {
		t.Fatalf("got %q, want %q", got, "orld!")
	}
	if requests != 1 {
		t.Fatalf("got %d requests, want 1 (absolute seek must not resolve the suffix)", requests)
	}
}

func TestSuffixSeekerInvalidSeekKeepsSuffix(t *testing.T) {
	ctx := context.Background()

	var requests int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSuffixSeeker(ctx, s.Client().Transport, req, 6)
	defer rsc.Close()

	// A failed seek must not corrupt the pending suffix range.
	if _, err := rsc.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("want error for negative offset")
	}

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
	if requests != 1 {
		t.Fatalf("got %d requests, want 1", requests)
	}
}

func TestSeekEndBeforeRead(t *testing.T) {
	ctx := context.Background()

	var requests int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	// SeekEnd with unknown size probes the server for the total size.
	offset, err := rsc.Seek(-6, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 6 {
		t.Fatalf("got offset %d, want 6", offset)
	}

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
	if requests != 1 {
		t.Fatalf("got %d requests, want 1 (probe reused via forward discard)", requests)
	}
}

func TestSeekEndEmpty(t *testing.T) {
	ctx := context.Background()

	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader(nil))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	offset, err := rsc.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 0 {
		t.Fatalf("got offset %d, want 0", offset)
	}
	if size := rsc.Size(); size != 0 {
		t.Fatalf("got size %d, want 0", size)
	}

	buf := make([]byte, 1)
	if n, err := rsc.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("got (%d, %v), want (0, io.EOF)", n, err)
	}
	if requests != 1 {
		t.Fatalf("got %d requests, want 1", requests)
	}
}

func TestUnknownSizeEOF(t *testing.T) {
	ctx := context.Background()

	// Flushing forces a chunked response without Content-Length.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Hello "))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("World!"))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatalf("want clean EOF for unknown size, got %v", err)
	}
	if string(got) != "Hello World!" {
		t.Fatalf("got %q, want %q", got, "Hello World!")
	}
}

func TestReadPastEnd(t *testing.T) {
	ctx := context.Background()

	var requests int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	if _, err := rsc.Seek(1000, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 4)
	n, err := rsc.Read(buf)
	if n != 0 || err != io.EOF {
		t.Fatalf("got (%d, %v), want (0, io.EOF)", n, err)
	}

	// The 416 result is cached; another read must not hit the server.
	if _, err := rsc.Read(buf); err != io.EOF {
		t.Fatalf("got %v, want io.EOF", err)
	}
	if requests != 1 {
		t.Fatalf("got %d requests, want 1", requests)
	}

	// The size learned from the 416 Content-Range enables SeekEnd recovery.
	offset, err := rsc.Seek(-6, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 6 {
		t.Fatalf("got offset %d, want 6", offset)
	}
	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
}

func TestUnsupportedResponseCached(t *testing.T) {
	ctx := context.Background()

	var requests int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "error")
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	buf := make([]byte, 1)
	if _, err := rsc.Read(buf); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
	if _, err := rsc.Read(buf); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
	if requests != 1 {
		t.Fatalf("got %d requests, want 1 (error must be cached)", requests)
	}

	resp, err := rsc.Response()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("got status %d, want 500", resp.StatusCode)
	}
	if resp.ContentLength != 0 {
		t.Fatalf("got ContentLength %d, want 0", resp.ContentLength)
	}
	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		t.Fatalf("got Content-Length header %q, want none", contentLength)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("got %q, want empty body", body)
	}
}

func TestContentChanged(t *testing.T) {
	ctx := context.Background()

	var mu sync.Mutex
	content := []byte("Hello World!")
	etag := `"v1"`
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c, e := content, etag
		mu.Unlock()
		w.Header().Set("ETag", e)
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader(c))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	buf := make([]byte, 5)
	if _, err := io.ReadFull(rsc, buf); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	content = []byte("Bye World!!!")
	etag = `"v2"`
	mu.Unlock()

	if _, err := rsc.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := rsc.Read(buf); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("got %v, want ErrContentChanged", err)
	}
}

func TestLateETagNotAChange(t *testing.T) {
	ctx := context.Background()

	// The first response carries only Last-Modified; later ones add an ETag.
	modTime := time.Unix(1700000000, 0)
	var requests int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			w.Header().Set("ETag", `"v1"`)
		}
		http.ServeContent(w, r, "test", modTime, bytes.NewReader([]byte("Hello World!")))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	buf := make([]byte, 5)
	if _, err := io.ReadFull(rsc, buf); err != nil {
		t.Fatal(err)
	}

	// Seeking backwards forces a new request whose response now has an ETag.
	if _, err := rsc.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatalf("a late ETag with unchanged Last-Modified must not be a content change: %v", err)
	}
	if string(got) != "Hello World!" {
		t.Fatalf("got %q, want %q", got, "Hello World!")
	}
	if requests != 2 {
		t.Fatalf("got %d requests, want 2", requests)
	}
}

func TestContentChangedWithoutNewValidator(t *testing.T) {
	ctx := context.Background()

	var mu sync.Mutex
	etag := `"v1"`
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		e := etag
		mu.Unlock()
		if ir := r.Header.Get("If-Range"); ir != "" && ir != e {
			// Changed content: honor If-Range with a full response carrying no validator.
			_, _ = w.Write([]byte("Bye World!!!"))
			return
		}
		w.Header().Set("ETag", e)
		http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	buf := make([]byte, 5)
	if _, err := io.ReadFull(rsc, buf); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	etag = `"v2"`
	mu.Unlock()

	// Backward seek forces a re-request; the full response must be classified as a change.
	if _, err := rsc.Seek(1, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := rsc.Read(buf); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("got %v, want ErrContentChanged", err)
	}
}

func TestRangeIgnoringServerNotAChange(t *testing.T) {
	ctx := context.Background()

	// The server never honors Range but echoes a stable ETag.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("Hello World!"))
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeeker(ctx, s.Client().Transport, req)
	defer rsc.Close()

	buf := make([]byte, 5)
	if _, err := io.ReadFull(rsc, buf); err != nil {
		t.Fatal(err)
	}

	if _, err := rsc.Seek(1, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	_, err = rsc.Read(buf)
	if !errors.Is(err, ErrCodeForByteRange) {
		t.Fatalf("got %v, want ErrCodeForByteRange", err)
	}
	if errors.Is(err, ErrContentChanged) {
		t.Fatalf("a matching validator must not be reported as a content change: %v", err)
	}
}

func TestSuffixSeekerRecoverAfter416(t *testing.T) {
	ctx := context.Background()

	// Empty file: any suffix range is unsatisfiable.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes */0")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSuffixSeeker(ctx, s.Client().Transport, req, 6)
	defer rsc.Close()

	buf := make([]byte, 4)
	if _, err := rsc.Read(buf); !errors.Is(err, ErrRangeNotSatisfiable) {
		t.Fatalf("got %v, want ErrRangeNotSatisfiable", err)
	}
	// A relative seek cannot resolve the position and must keep failing.
	if _, err := rsc.Seek(0, io.SeekCurrent); !errors.Is(err, ErrRangeNotSatisfiable) {
		t.Fatalf("got %v, want ErrRangeNotSatisfiable", err)
	}
	// An absolute seek escapes the failed suffix range.
	offset, err := rsc.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("SeekStart must recover after a suffix 416: %v", err)
	}
	if offset != 0 {
		t.Fatalf("got offset %d, want 0", offset)
	}
	if n, err := rsc.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("got (%d, %v), want (0, io.EOF)", n, err)
	}
}

func TestSeekWithHTTPClient(t *testing.T) {
	ctx := context.Background()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/r" {
			http.Redirect(w, r, "/d", http.StatusFound)
			return
		}
		if r.URL.Path == "/d" {
			http.ServeContent(w, r, "test", time.Time{}, bytes.NewReader([]byte("Hello World!")))
			return
		}
		http.NotFound(w, r)
	}))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/r", nil)
	if err != nil {
		t.Fatal(err)
	}
	rsc := NewSeekerWithHTTPClient(ctx, s.Client(), req)
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

# httpseek

[![Go Report Card](https://goreportcard.com/badge/github.com/wzshiming/httpseek)](https://goreportcard.com/report/github.com/wzshiming/httpseek)
[![GoDoc](https://godoc.org/github.com/wzshiming/httpseek?status.svg)](https://godoc.org/github.com/wzshiming/httpseek)
[![GitHub license](https://img.shields.io/github/license/wzshiming/httpseek.svg)](https://github.com/wzshiming/httpseek/blob/master/LICENSE)

This is a simple HTTP seeker that can be used to seek through a file using HTTP range requests.

## Usage

`Seeker` implements `io.ReadSeekCloser` on top of an HTTP endpoint:

``` go
req, _ := http.NewRequest(http.MethodGet, "https://example.com/large-file", nil)
rsc := httpseek.NewSeeker(context.Background(), http.DefaultTransport, req)
defer rsc.Close()

// Read/Seek like a local file; each jump is served by a Range request.
rsc.Seek(-16, io.SeekEnd)
io.ReadAll(rsc)
```

Also available: `NewRangeSeeker` (bounded range), `NewSuffixSeeker` (last N bytes),
and `NewSeekerWithHTTPClient` (follows redirects via an `*http.Client`).

`NewMustReaderTransport` wraps a `http.RoundTripper` so interrupted GET downloads
are transparently resumed with Range requests:

``` go
client := &http.Client{
    Transport: httpseek.NewMustReaderTransport(http.DefaultTransport,
        httpseek.RetryWithBackoff(3, time.Second)),
}
resp, _ := client.Get("https://example.com/large-file")
defer resp.Body.Close()
// resp.Body retries and resumes on transient read errors.
```

Content consistency across reconnects is guarded with `If-Range` and
ETag/Last-Modified validation; a changed file surfaces `ErrContentChanged`
instead of mixed content.

## MIT License

Licensed under the MIT License. See [LICENSE](https://github.com/wzshiming/httpseek/blob/master/LICENSE) for the full license text.

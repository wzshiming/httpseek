package httpseek

import (
	"io"
)

type mustReader struct {
	rsc          io.ReadSeeker
	errorHandler func(int, error) error
	offset       int
}

// NewMustReader returns a reader that will retry reading with partial byte ranges if the underlying reader returns an error.
func NewMustReader(rsc io.ReadSeeker, errorHandler func(int, error) error) io.Reader {
	return &mustReader{
		rsc:          rsc,
		errorHandler: errorHandler,
	}
}

// NewMustReadCloser returns a reader that will retry reading with partial byte ranges if the underlying reader returns an error.
func NewMustReadCloser(rsc io.ReadSeekCloser, errorHandler func(int, error) error) io.ReadCloser {
	return struct {
		io.Reader
		io.Closer
	}{
		Reader: NewMustReader(rsc, errorHandler),
		Closer: rsc,
	}
}

// Read reads from the reader.
func (r *mustReader) Read(p []byte) (n int, err error) {
	return r.read(0, p)
}

func (r *mustReader) read(retry int, p []byte) (n int, err error) {
	n, err = r.rsc.Read(p)
	r.offset += n
	if err == nil {
		return n, nil
	}

	if err == io.EOF {
		return n, err
	}

	if r.errorHandler != nil {
		if err = r.errorHandler(retry, err); err != nil {
			return n, err
		}
	}

	if n != 0 {
		return n, nil
	}
	return r.read(retry+1, p)
}

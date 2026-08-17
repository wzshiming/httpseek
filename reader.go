package httpseek

import (
	"io"
)

type mustReadSeeker struct {
	readSeeker   io.ReadSeeker
	errorHandler func(int, error) error
	offset       int64
}

// NewMustReadSeeker returns a reader that retries reading if the underlying reader
// returns an error. offset must be the current absolute position of rsc; it is used
// to restore the position before retrying. errorHandler receives the retry count and
// the error; returning a non-nil error stops retrying and is reported to the caller.
func NewMustReadSeeker(rsc io.ReadSeeker, offset int64, errorHandler func(int, error) error) io.ReadSeeker {
	return &mustReadSeeker{
		readSeeker:   rsc,
		offset:       offset,
		errorHandler: errorHandler,
	}
}

// NewMustReadSeekCloser returns a reader that will retry reading with partial byte ranges if the underlying reader returns an error.
func NewMustReadSeekCloser(rsc io.ReadSeekCloser, offset int64, errorHandler func(int, error) error) io.ReadSeekCloser {
	return struct {
		io.ReadSeeker
		io.Closer
	}{
		ReadSeeker: NewMustReadSeeker(rsc, offset, errorHandler),
		Closer:     rsc,
	}
}

func (r *mustReadSeeker) Seek(offset int64, whence int) (int64, error) {
	for retry := 0; ; retry++ {
		abs, err := r.readSeeker.Seek(offset, whence)
		if err == nil {
			r.offset = abs
			return abs, nil
		}
		if r.errorHandler == nil {
			return abs, err
		}
		if err = r.errorHandler(retry, err); err != nil {
			return 0, err
		}
	}
}

// Read reads from the reader.
func (r *mustReadSeeker) Read(p []byte) (n int, err error) {
	for retry := 0; ; retry++ {
		n, err = r.readSeeker.Read(p)
		r.offset += int64(n)
		if err == nil || err == io.EOF {
			return n, err
		}
		if r.errorHandler == nil {
			return n, err
		}
		if err = r.errorHandler(retry, err); err != nil {
			return n, err
		}
		if n != 0 {
			// Partial data was read; report it and let the next Read continue.
			return n, nil
		}
		// Restore the position in case the underlying reader drifted.
		if _, serr := r.readSeeker.Seek(r.offset, io.SeekStart); serr != nil {
			return 0, serr
		}
	}
}

package httpseek

import (
	"io"
)

type mustReadSeeker struct {
	readSeeker   io.ReadSeeker
	errorHandler func(int, error) error
	offset       int64
	err          error
}

// NewMustReadSeeker returns a reader that will retry reading with partial byte ranges if the underlying reader returns an error.
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
	abs, err := r.readSeeker.Seek(offset, whence)
	if err != nil {
		return abs, err
	}
	r.offset = abs
	r.err = err
	return abs, nil
}

// Read reads from the reader.
func (r *mustReadSeeker) Read(p []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}
	return r.read(0, p)
}

func (r *mustReadSeeker) read(retry int, p []byte) (n int, err error) {
	n, err = r.readSeeker.Read(p)

	r.offset += int64(n)
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

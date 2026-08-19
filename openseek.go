package httpseek

import (
	"errors"
	"fmt"
	"io"
)

// maxSkipBytes is the largest forward-seek distance served by discarding
// bytes from the current stream instead of a fresh open.
const maxSkipBytes = 256 << 10

var _ io.ReadSeekCloser = (*OpenSeeker)(nil)

// Opener provides ranged access to a resource for building an io.ReadSeekCloser.
type Opener interface {
	// OpenRange opens the byte range [start, end] of the resource, where end
	// is the inclusive end byte and -1 means open-ended (read to EOF);
	// start 0 with end -1 opens the whole resource.
	// A negative start requests the suffix of length end; the resolved range
	// is reported back in the result.
	// Implementations report ErrRangeNotSatisfiable for a range beyond EOF and
	// ErrUnsupported for other unsupported opens; the result's Size stays
	// meaningful alongside these two errors, and its Validator alongside
	// ErrRangeNotSatisfiable.
	// A non-empty validator argument requires the content to still match it;
	// implementations report ErrContentChanged when it does not, or may just
	// report the current version in Validator for the caller to compare.
	OpenRange(validator string, start, end int64) (OpenResult, error)

	// Size reports the total size of the resource in the result; -1 if unknown.
	// The validator argument behaves as in OpenRange, and the result's
	// Validator reports the content version as in OpenResult.
	// Implementations without a cheap probe may report ErrUnsupported or a -1
	// size; the OpenSeeker then falls back to learning the size from an
	// OpenRange result.
	Size(validator string) (SizeResult, error)
}

// OpenResult is the outcome of an OpenRange call.
type OpenResult struct {
	// Body streams the opened range; nil when the open failed.
	Body io.ReadCloser
	// Start and End are the resolved byte range; End is the inclusive
	// end byte, -1 for open-ended.
	Start, End int64
	// Size is the total size of the resource; -1 if unknown.
	Size int64
	// Validator identifies the content version; empty if none.
	Validator string
}

// SizeResult is the outcome of a Size call.
type SizeResult struct {
	// Size is the total size of the resource; -1 if unknown.
	Size int64
	// Validator identifies the content version; empty if none.
	Validator string
}

// NewOpenSeeker assembles an io.ReadSeekCloser on top of an Opener,
// reading the whole resource.
// A validator captured from the first open is passed to later opens so the
// Opener can detect content changes.
func NewOpenSeeker(opener Opener) *OpenSeeker {
	return NewRangeOpenSeeker(opener, 0, -1)
}

// NewSuffixOpenSeeker assembles an io.ReadSeekCloser that reads the last
// suffixLen bytes of the resource. The actual byte range is resolved by the
// Opener on the first open.
func NewSuffixOpenSeeker(opener Opener, suffixLen int64) *OpenSeeker {
	return NewRangeOpenSeeker(opener, -1, suffixLen)
}

// NewRangeOpenSeeker assembles an io.ReadSeekCloser that reads a bounded
// byte range [start, end] of the resource.
// end is the inclusive end byte; -1 means open-ended (read to EOF).
// A negative start requests the suffix of length end, as in Opener.OpenRange.
func NewRangeOpenSeeker(opener Opener, start, end int64) *OpenSeeker {
	return &OpenSeeker{
		opener: opener,
		offset: start,
		size:   -1,
		end:    end,
	}
}

// OpenSeeker adapts an Opener into an io.ReadSeekCloser.
// It is not safe for concurrent use.
type OpenSeeker struct {
	opener Opener

	rc        io.ReadCloser
	offset    int64
	size      int64
	sizeKnown bool
	end       int64  // inclusive end byte for range reads; -1 for open-ended
	validator string // content version captured on the first open and required on re-opens
	err       error  // classification of the last unsupported open
}

func (s *OpenSeeker) Read(p []byte) (n int, err error) {
	if s.rc == nil && s.err != nil {
		// The sticky error outranks the EOF fast path: an unsupported open may
		// report a size placing the offset at EOF, yet reads must keep failing.
		return 0, s.stickyErr()
	}

	// Already at the end of the requested range or known resource size.
	if s.offset >= 0 {
		if s.end >= 0 {
			if s.offset > s.end {
				return 0, io.EOF
			}
		} else if s.sizeKnown && s.offset >= s.size {
			return 0, io.EOF
		}
	}

	if s.rc == nil {
		err = s.open()
		if err != nil {
			return 0, err
		}
		if s.rc == nil {
			if err = s.stickyErr(); err != nil {
				return 0, err
			}
			return 0, ErrUnsupported
		}
	}

	if s.end >= 0 {
		// If the end byte is known, limit the read to the remaining bytes in the range.
		if remaining := s.end - s.offset; len(p) > 0 && int64(len(p)-1) > remaining {
			p = p[:remaining+1]
		}
	}

	n, err = s.rc.Read(p)
	s.offset += int64(n)
	if err != nil {
		// For range reads, determine the effective end; for whole reads, use total size.
		atEnd := false
		switch {
		case s.end >= 0:
			atEnd = s.offset > s.end || s.sizeKnown && s.offset >= s.size
		case s.sizeKnown:
			atEnd = s.offset >= s.size
		default:
			// Total size unknown: trust the source's EOF.
			atEnd = err == io.EOF
		}
		if !atEnd {
			_ = s.reset()
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
		}
	}
	return n, err
}

// stickyErr reports the cached error from the last unsupported open.
// A range beyond EOF reads as io.EOF, matching file semantics.
func (s *OpenSeeker) stickyErr() error {
	if errors.Is(s.err, ErrRangeNotSatisfiable) && s.offset >= 0 {
		return io.EOF
	}
	return s.err
}

// Seek sets the offset for the next Read to the specified offset.
// Seeking to a different offset clears the error classification cached from
// the last unsupported open; seeking to the current offset preserves it.
func (s *OpenSeeker) Seek(offset int64, whence int) (int64, error) {
	if s.offset < 0 && whence != io.SeekStart {
		// Unresolved suffix range: relative seeks need the actual offset from the Opener.
		if s.err == nil {
			if err := s.open(); err != nil {
				return 0, err
			}
		}
		if s.offset < 0 {
			// Still unresolved (e.g. beyond EOF on an empty resource): only an absolute seek escapes.
			if s.err != nil {
				return 0, s.err
			}
			return 0, ErrUnsupported
		}
	}

	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = s.offset + offset
	case io.SeekEnd:
		if !s.sizeKnown {
			if err := s.probeSize(); err != nil {
				return 0, err
			}
		}
		newOffset = s.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if newOffset < 0 {
		return 0, errors.New("negative offset")
	}

	if s.offset < 0 {
		// Absolute seek escapes an unresolved suffix range; its end is a length, not a position.
		s.end = -1
	}
	if s.offset != newOffset {
		// Serve short forward seeks by discarding from the current stream.
		if s.rc != nil && newOffset > s.offset && newOffset-s.offset <= maxSkipBytes && (s.end < 0 || newOffset-1 <= s.end) {
			if _, err := io.CopyN(io.Discard, s.rc, newOffset-s.offset); err == nil {
				s.offset = newOffset
				return newOffset, nil
			}
			// Discard failed; fall back to a fresh open.
		}
		_ = s.reset()
		s.offset = newOffset
		s.err = nil
	}
	return newOffset, nil
}

// probeSize learns the total size, preferring the direct Size probe and
// falling back to an open at the current offset, whose result may report
// the size (e.g. a Content-Range total).
func (s *OpenSeeker) probeSize() error {
	res, err := s.opener.Size(s.validator)
	if err == nil {
		if verr := s.checkValidator(res.Validator); verr != nil {
			_ = s.reset()
			return verr
		}
		if res.Size >= 0 {
			s.size = res.Size
			s.sizeKnown = true
			return nil
		}
	} else if !errors.Is(err, ErrUnsupported) {
		return err
	}
	// The probe was inconclusive; an open may still report the size.
	if err := s.open(); err != nil {
		return err
	}
	if !s.sizeKnown {
		if s.err != nil {
			// The open knows no better; report its cached failure.
			return s.err
		}
		return ErrSizeUnknown
	}
	return nil
}

// open performs the open matching the current offset and range bounds.
func (s *OpenSeeker) open() error {
	res, err := s.opener.OpenRange(s.validator, s.offset, s.end)
	if err != nil && res.Body != nil {
		// A failed open carries no stream per the contract; close a stray one.
		_ = res.Body.Close()
		res.Body = nil
	}
	if err != nil && !errors.Is(err, ErrRangeNotSatisfiable) && !errors.Is(err, ErrUnsupported) {
		return err
	}
	if verr := s.checkValidator(res.Validator); verr != nil {
		if res.Body != nil {
			_ = res.Body.Close()
		}
		// Drop any live stream: after ErrContentChanged reads must keep failing.
		_ = s.reset()
		return verr
	}
	_ = s.reset()
	if err != nil {
		// Unsupported open: remember it so Read can report a meaningful error.
		s.err = err
	} else {
		s.err = nil
		s.rc = res.Body
		s.offset = res.Start
		s.end = res.End
	}
	if res.Size >= 0 {
		// A size may be learned even from a beyond-EOF open.
		s.size = res.Size
		s.sizeKnown = true
	}
	return nil
}

// checkValidator captures the first reported content version, so later opens
// can detect content changes, and rejects a version differing from it.
func (s *OpenSeeker) checkValidator(v string) error {
	if s.validator == "" {
		s.validator = v
		return nil
	}
	if v != "" && v != s.validator {
		return fmt.Errorf("%w: validator changed from %q to %q", ErrContentChanged, s.validator, v)
	}
	return nil
}

// Close closes the OpenSeeker.
func (s *OpenSeeker) Close() error {
	return s.reset()
}

// OK indicates whether the OpenSeeker is ready to be read.
func (s *OpenSeeker) OK() bool {
	return s.rc != nil
}

// Size returns the last known total size of the resource; -1 if unknown.
func (s *OpenSeeker) Size() int64 {
	return s.size
}

// Offset returns the current offset of the OpenSeeker.
func (s *OpenSeeker) Offset() int64 {
	return s.offset
}

func (s *OpenSeeker) reset() error {
	if s.rc == nil {
		return nil
	}
	err := s.rc.Close()
	s.rc = nil
	return err
}

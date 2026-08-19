package httpseek

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

var _ Opener = (*testOpener)(nil)

type testOpener struct {
	content        []byte
	validator      string
	sizeKnown      bool
	probeSizeKnown bool // Size reports the total even when opens do not
	opens          int  // whole-resource opens: start 0 and open-ended
	rangeOpens     int
}

func (o *testOpener) OpenRange(validator string, start, end int64) (OpenResult, error) {
	if start == 0 && end < 0 {
		o.opens++
	} else {
		o.rangeOpens++
	}
	total := int64(len(o.content))
	if validator != "" && validator != o.validator {
		return OpenResult{Size: -1, Start: start, End: end}, ErrContentChanged
	}
	if start < 0 {
		// Suffix of length end.
		if end > total {
			end = total
		}
		start, end = total-end, total-1
	}
	if start >= total {
		// Beyond EOF: like HTTP 416, the total size is still reported.
		return OpenResult{Size: total, Start: start, End: end, Validator: o.validator}, ErrRangeNotSatisfiable
	}
	if end < 0 || end >= total {
		end = total - 1
	}
	size := int64(-1)
	if o.sizeKnown {
		size = total
	}
	return OpenResult{
		Body:      io.NopCloser(bytes.NewReader(o.content[start : end+1])),
		Size:      size,
		Start:     start,
		End:       end,
		Validator: o.validator,
	}, nil
}

func (o *testOpener) Size(validator string) (SizeResult, error) {
	if validator != "" && validator != o.validator {
		return SizeResult{Size: -1}, ErrContentChanged
	}
	if !o.sizeKnown && !o.probeSizeKnown {
		return SizeResult{Size: -1, Validator: o.validator}, nil
	}
	return SizeResult{Size: int64(len(o.content)), Validator: o.validator}, nil
}

func TestOpenSeeker(t *testing.T) {
	content := []byte("Hello World!")
	opener := &testOpener{content: content, sizeKnown: true}

	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
	if opener.opens != 1 || opener.rangeOpens != 0 {
		t.Fatalf("got opens=%d rangeOpens=%d, want 1 and 0", opener.opens, opener.rangeOpens)
	}

	if _, err = rsc.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if got, err = io.ReadAll(rsc); err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}

	if _, err = rsc.Seek(-6, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	if got, err = io.ReadAll(rsc); err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}

	if _, err = rsc.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err = rsc.Seek(6, io.SeekCurrent); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err = io.ReadFull(rsc, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "World" {
		t.Fatalf("got %q, want %q", buf, "World")
	}
}

func TestOpenSeekerUnknownSize(t *testing.T) {
	content := []byte("Hello World!")
	rsc := NewOpenSeeker(&testOpener{content: content})
	defer rsc.Close()

	if _, err := rsc.Seek(0, io.SeekEnd); !errors.Is(err, ErrSizeUnknown) {
		t.Fatalf("got %v, want ErrSizeUnknown", err)
	}

	if _, err := rsc.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
}

func TestOpenSeekerContentChanged(t *testing.T) {
	opener := &testOpener{content: []byte("Hello World!"), validator: `"v1"`, sizeKnown: true}
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	if _, err := io.ReadAll(rsc); err != nil {
		t.Fatal(err)
	}

	// Content changes between opens; the captured validator must reject it.
	opener.content = []byte("Goodbye World!")
	opener.validator = `"v2"`

	if _, err := rsc.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rsc); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("got %v, want ErrContentChanged", err)
	}

	if _, err := rsc.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rsc); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("got %v, want ErrContentChanged", err)
	}
}

func TestRangeOpenSeeker(t *testing.T) {
	content := []byte("Hello World!")
	opener := &testOpener{content: content, sizeKnown: true}
	rsc := NewRangeOpenSeeker(opener, 3, 8)
	defer rsc.Close()

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "lo Wor" {
		t.Fatalf("got %q, want %q", got, "lo Wor")
	}

	if _, err = rsc.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if got, err = io.ReadAll(rsc); err != nil {
		t.Fatal(err)
	}
	if string(got) != "Wor" {
		t.Fatalf("got %q, want %q", got, "Wor")
	}

	// io.SeekEnd is relative to the whole resource; past the range end reads EOF.
	if _, err = rsc.Seek(-2, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	if got, err = io.ReadAll(rsc); err != nil || len(got) != 0 {
		t.Fatalf("got %q err=%v, want empty and nil", got, err)
	}
}

func TestSuffixOpenSeeker(t *testing.T) {
	content := []byte("Hello World!")
	opener := &testOpener{content: content, sizeKnown: true}
	rsc := NewSuffixOpenSeeker(opener, 6)
	defer rsc.Close()

	// A relative seek resolves the suffix range first.
	off, err := rsc.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if off != 6 {
		t.Fatalf("got offset %d, want 6", off)
	}

	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}

	if _, err = rsc.Seek(-6, io.SeekCurrent); err != nil {
		t.Fatal(err)
	}
	if got, err = io.ReadAll(rsc); err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
}

// unsupportedOpener fails every open with ErrUnsupported while still
// reporting a valid size, as the Opener contract allows.
type unsupportedOpener struct {
	size int64
}

func (o *unsupportedOpener) OpenRange(_ string, start, end int64) (OpenResult, error) {
	return OpenResult{Size: o.size, Start: start, End: end}, ErrUnsupported
}

func (o *unsupportedOpener) Size(string) (SizeResult, error) {
	return SizeResult{Size: o.size}, nil
}

func TestOpenSeekerUnsupportedSticky(t *testing.T) {
	// The failed open reports size 0, placing offset 0 at EOF; the cached
	// ErrUnsupported must still outrank the EOF fast path on repeated reads.
	rsc := NewOpenSeeker(&unsupportedOpener{size: 0})
	defer rsc.Close()

	buf := make([]byte, 1)
	if _, err := rsc.Read(buf); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
	if _, err := rsc.Read(buf); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported on repeated read", err)
	}
}

// strayBodyOpener violates the Opener contract by returning a body
// alongside a failed open.
type strayBodyOpener struct {
	closed bool
}

func (o *strayBodyOpener) OpenRange(_ string, start, end int64) (OpenResult, error) {
	return OpenResult{Body: strayBody{&o.closed}, Size: -1, Start: start, End: end}, ErrUnsupported
}

func (o *strayBodyOpener) Size(string) (SizeResult, error) {
	return SizeResult{Size: -1}, nil
}

type strayBody struct{ closed *bool }

func (b strayBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b strayBody) Close() error             { *b.closed = true; return nil }

func TestOpenSeekerClosesStrayBodyOnFailedOpen(t *testing.T) {
	opener := &strayBodyOpener{}
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	if _, err := rsc.Read(make([]byte, 1)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
	if !opener.closed {
		t.Fatal("stray body was not closed")
	}
}

// unsupportedSizeOpener rejects the direct size probe; only opens report size.
type unsupportedSizeOpener struct{ *testOpener }

func (o *unsupportedSizeOpener) Size(string) (SizeResult, error) {
	return SizeResult{Size: -1}, ErrUnsupported
}

func TestOpenSeekerSizeFallbackToOpen(t *testing.T) {
	content := []byte("Hello World!")
	opener := &testOpener{content: content, sizeKnown: true}
	rsc := NewOpenSeeker(&unsupportedSizeOpener{opener})
	defer rsc.Close()

	off, err := rsc.Seek(-6, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	if off != 6 {
		t.Fatalf("got offset %d, want 6", off)
	}
	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
	// The fallback open doubles as the read stream via forward discard.
	if opener.opens != 1 || opener.rangeOpens != 0 {
		t.Fatalf("got opens=%d rangeOpens=%d, want 1 and 0", opener.opens, opener.rangeOpens)
	}
}

// fatalSizeOpener fails the size probe with a non-sentinel error.
type fatalSizeOpener struct{ *testOpener }

var errProbeFatal = errors.New("probe fatal")

func (o *fatalSizeOpener) Size(string) (SizeResult, error) {
	return SizeResult{Size: -1}, errProbeFatal
}

func TestOpenSeekerSizeProbeFatalError(t *testing.T) {
	opener := &testOpener{content: []byte("Hello World!"), sizeKnown: true}
	rsc := NewOpenSeeker(&fatalSizeOpener{opener})
	defer rsc.Close()

	if _, err := rsc.Seek(0, io.SeekEnd); !errors.Is(err, errProbeFatal) {
		t.Fatalf("got %v, want errProbeFatal", err)
	}
	// A fatal probe error must not trigger the fallback open.
	if opener.opens != 0 || opener.rangeOpens != 0 {
		t.Fatalf("got opens=%d rangeOpens=%d, want no opens", opener.opens, opener.rangeOpens)
	}
}

// fatalOpenOpener has no size probe and fails every open fatally.
type fatalOpenOpener struct{}

var errOpenFatal = errors.New("open fatal")

func (fatalOpenOpener) OpenRange(string, int64, int64) (OpenResult, error) {
	return OpenResult{Size: -1, Start: 0, End: -1}, errOpenFatal
}

func (fatalOpenOpener) Size(string) (SizeResult, error) {
	return SizeResult{Size: -1}, ErrUnsupported
}

func TestOpenSeekerSizeFallbackOpenError(t *testing.T) {
	rsc := NewOpenSeeker(fatalOpenOpener{})
	defer rsc.Close()

	if _, err := rsc.Seek(0, io.SeekEnd); !errors.Is(err, errOpenFatal) {
		t.Fatalf("got %v, want errOpenFatal", err)
	}
}

func TestOpenSeekerSeekEndUnsatisfiableNoTotal(t *testing.T) {
	content := []byte("Hello World!")
	// Neither the probe nor the beyond-EOF fallback open reveals a total.
	opener := &noSize416Opener{&testOpener{content: content}}
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	if _, err := rsc.Seek(100, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := rsc.Seek(0, io.SeekEnd); !errors.Is(err, ErrRangeNotSatisfiable) {
		t.Fatalf("got %v, want ErrRangeNotSatisfiable", err)
	}

	// The failure does not poison the seeker; an absolute seek recovers.
	if _, err := rsc.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
}

// truncatingOpener serves fewer bytes than the resolved range promises.
type truncatingOpener struct {
	*testOpener
	limit int64
}

func (o *truncatingOpener) OpenRange(validator string, start, end int64) (OpenResult, error) {
	res, err := o.testOpener.OpenRange(validator, start, end)
	if res.Body != nil {
		res.Body = io.NopCloser(io.LimitReader(res.Body, o.limit))
	}
	return res, err
}

func TestOpenSeekerTruncatedStream(t *testing.T) {
	content := []byte("Hello World!")
	opener := &truncatingOpener{testOpener: &testOpener{content: content, sizeKnown: true}, limit: 5}
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	buf := make([]byte, 8)
	n, err := rsc.Read(buf)
	if n != 5 || err != nil {
		t.Fatalf("got n=%d err=%v, want 5 and nil", n, err)
	}
	if string(buf[:n]) != "Hello" {
		t.Fatalf("got %q, want %q", buf[:n], "Hello")
	}

	// EOF before the promised end is a truncation, not a normal end.
	if _, err := rsc.Read(buf); err != io.ErrUnexpectedEOF {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}

	// The next read resumes from a fresh open at the same offset.
	n, err = rsc.Read(buf)
	if n != 5 || err != nil {
		t.Fatalf("got n=%d err=%v, want 5 and nil", n, err)
	}
	if string(buf[:n]) != " Worl" {
		t.Fatalf("got %q, want %q", buf[:n], " Worl")
	}
}

func TestOpenSeekerSizeProbe(t *testing.T) {
	content := []byte("Hello World!")
	// Opens never report a size; only the direct Size probe knows it.
	opener := &testOpener{content: content, probeSizeKnown: true}
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	off, err := rsc.Seek(-6, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	if off != 6 {
		t.Fatalf("got offset %d, want 6", off)
	}
	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
	// The probe opened nothing; the read is served by a single range open.
	if opener.opens != 0 || opener.rangeOpens != 1 {
		t.Fatalf("got opens=%d rangeOpens=%d, want 0 and 1", opener.opens, opener.rangeOpens)
	}
}

func TestOpenSeekerBeyondEOF(t *testing.T) {
	content := []byte("Hello World!")
	opener := &testOpener{content: content} // size not reported on successful opens
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	if _, err := rsc.Seek(100, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if n, err := rsc.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("got n=%d err=%v, want 0 and io.EOF", n, err)
	}

	// The size learned from the beyond-EOF open answers io.SeekEnd.
	off, err := rsc.Seek(-6, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	if off != 6 {
		t.Fatalf("got offset %d, want 6", off)
	}
	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
}

// noSize416Opener strips the size a beyond-EOF open would report, leaving
// the direct Size probe as the only source.
type noSize416Opener struct{ *testOpener }

func (o *noSize416Opener) OpenRange(validator string, start, end int64) (OpenResult, error) {
	res, err := o.testOpener.OpenRange(validator, start, end)
	if err != nil {
		res.Size = -1
	}
	return res, err
}

func TestOpenSeekerValidatorFromBeyondEOFOpen(t *testing.T) {
	opener := &testOpener{content: []byte("Hello World!"), validator: `"v1"`}
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	// The first contact is a beyond-EOF open; its result reveals the validator.
	if _, err := rsc.Seek(100, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if n, err := rsc.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("got n=%d err=%v, want 0 and io.EOF", n, err)
	}

	// Content changes; the captured validator must reject the reopen.
	opener.content = []byte("Goodbye World!")
	opener.validator = `"v2"`

	if _, err := rsc.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rsc); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("got %v, want ErrContentChanged", err)
	}
}

// sizeValidatorOpener reveals the validator only through the Size probe.
type sizeValidatorOpener struct{ *testOpener }

func (o *sizeValidatorOpener) OpenRange(validator string, start, end int64) (OpenResult, error) {
	res, err := o.testOpener.OpenRange(validator, start, end)
	res.Validator = ""
	return res, err
}

func TestOpenSeekerValidatorFromSizeProbe(t *testing.T) {
	opener := &sizeValidatorOpener{&testOpener{content: []byte("Hello World!"), validator: `"v1"`, probeSizeKnown: true}}
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	// Only the direct size probe resolves the size and reveals the validator.
	if _, err := rsc.Seek(-6, io.SeekEnd); err != nil {
		t.Fatal(err)
	}

	// Content changes; the validator captured from the probe must reject the reopen.
	opener.content = []byte("Goodbye World!")
	opener.validator = `"v2"`

	if _, err := rsc.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rsc); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("got %v, want ErrContentChanged", err)
	}
}

// reportOnlyOpener never enforces the validator argument; it only reports
// the current version, leaving mismatch detection to the OpenSeeker.
type reportOnlyOpener struct{ *testOpener }

func (o *reportOnlyOpener) OpenRange(_ string, start, end int64) (OpenResult, error) {
	return o.testOpener.OpenRange("", start, end)
}

func (o *reportOnlyOpener) Size(string) (SizeResult, error) {
	return o.testOpener.Size("")
}

func TestOpenSeekerValidatorMismatch(t *testing.T) {
	// The Opener only reports the current version; the OpenSeeker itself must
	// reject a version differing from the captured one.
	opener := &reportOnlyOpener{&testOpener{content: []byte("Hello World!"), validator: `"v1"`, probeSizeKnown: true}}
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	// The first open captures the validator.
	if _, err := io.ReadFull(rsc, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}

	opener.content = []byte("Goodbye World!")
	opener.validator = `"v2"`

	if _, err := rsc.Seek(0, io.SeekEnd); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("got %v, want ErrContentChanged from the size probe", err)
	}
	if _, err := rsc.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rsc); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("got %v, want ErrContentChanged from the reopen", err)
	}
}

func TestOpenSeekerSizeProbeAfterUnsatisfiableRange(t *testing.T) {
	content := []byte("Hello World!")
	opener := &noSize416Opener{&testOpener{content: content, probeSizeKnown: true}}
	rsc := NewOpenSeeker(opener)
	defer rsc.Close()

	if _, err := rsc.Seek(100, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if n, err := rsc.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("got n=%d err=%v, want 0 and io.EOF", n, err)
	}

	// The cached range failure must not block the direct size probe.
	off, err := rsc.Seek(-6, io.SeekEnd)
	if err != nil {
		t.Fatal(err)
	}
	if off != 6 {
		t.Fatalf("got offset %d, want 6", off)
	}
	got, err := io.ReadAll(rsc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "World!" {
		t.Fatalf("got %q, want %q", got, "World!")
	}
}

package exchange

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// Fatal errors of a limited stream.
var (
	// ErrLineTooLong stops a stream at a line longer than Limits.MaxLineBytes.
	ErrLineTooLong = errors.New("line too long")
	// ErrTooMuchReported stops a stream that would report more than Limits.MaxReported
	// things.
	ErrTooMuchReported = errors.New("too much to report")
)

// Limits bound what reading one stream holds in memory, for a stream from someone not
// trusted with this process's memory. A zero field is no limit.
type Limits struct {
	// MaxLineBytes is the longest line, not counting its newline. A longer line stops the
	// stream with ErrLineTooLong before the rest of it is read.
	MaxLineBytes int
	// MaxReported is how many things the stream may report: headers, malformed lines and
	// distinct dropped entries together. Each is kept until the stream ends, so a stream
	// of many short lines could otherwise grow them without bound. One more stops the
	// stream with ErrTooMuchReported.
	MaxReported int
	// MaxReportedBytes clips each string the report keeps to this many bytes, plus an
	// ellipsis: a dropped entry's path and value, a malformed line's error and a header's
	// producer. A clipped malformed line's error no longer wraps its cause.
	MaxReportedBytes int
}

// Clip clips s as the report clips what it keeps, for a caller that adds to the report.
func (l Limits) Clip(s string) string {
	return clip(s, l.MaxReportedBytes)
}

// NewLimitedReader is NewReader within limits.
func NewLimitedReader(src io.Reader, limits Limits) *Reader {
	r := NewReader(src)
	r.limits = limits

	return r
}

// readLine reads one line, newline included, refusing one longer than the limit as soon
// as it has read that much of it.
func (r *Reader) readLine() ([]byte, error) {
	if r.limits.MaxLineBytes <= 0 {
		return r.src.ReadBytes('\n')
	}

	var line []byte

	for {
		chunk, err := r.src.ReadSlice('\n')

		content := len(line) + len(chunk)
		if err == nil {
			content-- // the newline
		}

		if content > r.limits.MaxLineBytes {
			return nil, fmt.Errorf("%w: longer than %d bytes", ErrLineTooLong, r.limits.MaxLineBytes)
		}

		line = append(line, chunk...)

		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

// admit adds a decoded line's drops to the report, together with listing more headers
// or malformed lines, unless that would report more than the limit allows. It then stops
// the stream instead, leaving the report as it was.
func (r *Reader) admit(n *notes, listing int) error {
	clipped := n.clipped(r.limits.MaxReportedBytes)

	if r.limits.MaxReported > 0 {
		added := listing + len(r.report.unseen(clipped))
		if r.listed+len(r.report)+added > r.limits.MaxReported {
			r.fatal = &FatalError{Line: r.line, Err: fmt.Errorf(
				"%w: more than %d headers, malformed lines and distinct dropped entries",
				ErrTooMuchReported, r.limits.MaxReported,
			)}

			return r.fatal
		}
	}

	r.listed += listing
	r.report.merge(clipped)

	return nil
}

// malformed reports a line skipped for err, or stops the stream if that is one more than
// the limit allows.
func (r *Reader) malformed(err error) error {
	if fatal := r.admit(&notes{}, 1); fatal != nil {
		return fatal
	}

	return &MalformedLineError{Line: r.line, Err: clipError(err, r.limits.MaxReportedBytes)}
}

// clip shortens s to at most n bytes, cut at a character boundary, and marks the cut. Zero
// is no limit.
func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}

	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut] + "…"
}

// clipHeader clips the header's free-text strings.
func clipHeader(h *Header, n int) {
	if n <= 0 || !h.Producer.Valid {
		return
	}

	h.Producer.Val.Program.Val = clip(h.Producer.Val.Program.Val, n)
	h.Producer.Val.Version.Val = clip(h.Producer.Val.Version.Val, n)
}

// clipError clips a malformed line's error. It drops the cause, which holds the
// whole message.
func clipError(err error, n int) error {
	if n <= 0 {
		return err
	}

	if msg := err.Error(); len(msg) > n {
		return errors.New(clip(msg, n))
	}

	return err
}

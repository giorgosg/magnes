package exchange

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Fatal stream errors. Reading cannot continue after one of these.
var (
	ErrRecordBeforeHeader = errors.New("record before the first header")
	ErrUnsupportedFormat  = errors.New("unsupported format name")
	ErrUnsupportedVersion = errors.New("unsupported major version")
	ErrInvalidHeader      = errors.New("invalid header")
)

// FatalError stops the stream. Next returns it again on every later call.
type FatalError struct {
	Line int
	Err  error
}

func (e *FatalError) Error() string { return fmt.Sprintf("line %d: %v", e.Line, e.Err) }

func (e *FatalError) Unwrap() error { return e.Err }

// MalformedLineError reports a line that is not a valid header or record. The line is
// skipped, and reading can continue.
type MalformedLineError struct {
	Line int
	Err  error
}

func (e *MalformedLineError) Error() string {
	return fmt.Sprintf("line %d: malformed: %v", e.Line, e.Err)
}

func (e *MalformedLineError) Unwrap() error { return e.Err }

// Line is one decoded line: a header or a record, never both.
type Line struct {
	Number int
	Header *Header
	Record *Record
}

// Reader decodes an exchange stream line by line.
type Reader struct {
	src       *bufio.Reader
	line      int
	header    *Header
	fatal     error
	exhausted bool
	report    report
	limits    Limits
	// listed counts the headers and malformed lines returned, which the caller keeps.
	listed int
}

func NewReader(src io.Reader) *Reader {
	return &Reader{src: bufio.NewReader(src), report: make(report)}
}

// Next returns the next header or record, io.EOF at the end of the stream, a
// *MalformedLineError for a line it skipped, or a *FatalError. Blank lines are ignored.
func (r *Reader) Next() (Line, error) {
	for {
		if r.fatal != nil {
			return Line{}, r.fatal
		}

		if r.exhausted {
			return Line{}, io.EOF
		}

		data, err := r.readLine()
		if errors.Is(err, io.EOF) {
			r.exhausted = true
		} else if err != nil {
			r.fatal = &FatalError{Line: r.line + 1, Err: err}
			return Line{}, r.fatal
		}

		if len(data) == 0 && r.exhausted {
			return Line{}, io.EOF
		}

		r.line++

		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}

		return r.decodeLine(data)
	}
}

func (r *Reader) decodeLine(data []byte) (Line, error) {
	line := Line{Number: r.line}

	n := &notes{scope: ScopeRecord}

	fields, err := newObject(n, "", data)
	if err != nil {
		return line, r.malformed(err)
	}

	if _, isHeader := fields.fields["format"]; isHeader {
		n.scope = ScopeHeader

		header, err := decodeHeader(n, fields)
		if err != nil {
			r.fatal = &FatalError{Line: r.line, Err: err}
			return line, r.fatal
		}

		if err := r.admit(n, 1); err != nil {
			return line, err
		}

		clipHeader(&header, r.limits.MaxReportedBytes)

		r.header = &header

		line.Header = &header

		return line, nil
	}

	if r.header == nil {
		r.fatal = &FatalError{Line: r.line, Err: ErrRecordBeforeHeader}
		return line, r.fatal
	}

	record, err := decodeRecord(fields)
	if err != nil {
		return line, r.malformed(err)
	}

	if err := r.admit(n, 0); err != nil {
		return line, err
	}

	line.Record = &record

	return line, nil
}

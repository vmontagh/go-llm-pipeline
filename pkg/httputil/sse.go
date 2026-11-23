package httputil

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"
)

// SSEData yields the data of each server-sent event in r, joining an
// event's data lines with newlines. Events without data, comments, and
// other fields are skipped. An event cut off by the end of r is not
// yielded; SSEData reports io.ErrUnexpectedEOF instead.
func SSEData(r io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		lr := lineReader{br: bufio.NewReader(r)}
		var data []byte
		hasData := false
		for {
			line, err := lr.next()
			if err != nil && !errors.Is(err, io.EOF) {
				yield(nil, fmt.Errorf("reading event: %w", err))
				return
			}
			if len(line) == 0 && err == nil {
				if hasData && !yield(data, nil) {
					return
				}
				data, hasData = nil, false
				continue
			}
			if name, value, _ := bytes.Cut(line, []byte(":")); string(name) == "data" {
				if hasData {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(value, []byte(" "))...)
				hasData = true
			}
			if err != nil {
				if hasData {
					yield(nil, fmt.Errorf("reading event: %w", io.ErrUnexpectedEOF))
				}
				return
			}
		}
	}
}

// lineReader reads lines ended by LF, CR, or CRLF, as SSE allows.
type lineReader struct {
	br *bufio.Reader
	// afterCR drops the LF of a CRLF when it comes, so a line ended by CR
	// is returned without waiting for the next byte.
	afterCR bool
}

// next returns the next line without its ending. At the end of the input
// it returns the unended rest and the read error.
func (lr *lineReader) next() ([]byte, error) {
	var line []byte
	for {
		b, err := lr.br.ReadByte()
		if err != nil {
			return line, err
		}
		if lr.afterCR {
			lr.afterCR = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\n':
			return line, nil
		case '\r':
			lr.afterCR = true
			return line, nil
		}
		line = append(line, b)
	}
}

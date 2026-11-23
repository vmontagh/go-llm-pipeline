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
		br := bufio.NewReader(r)
		var data []byte
		hasData := false
		for {
			line, err := br.ReadBytes('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				yield(nil, fmt.Errorf("reading event: %w", err))
				return
			}
			line = bytes.TrimRight(line, "\r\n")
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

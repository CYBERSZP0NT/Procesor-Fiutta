package openailike

import (
	"bufio"
	"io"
	"strings"
)

// SSEReader is a minimal Server-Sent-Events reader for OpenAI-compatible streams.
// It handles `data:` lines, `event:` lines (ignored), comments, and blank-line
// separators. Multiple `data:` lines within one event are joined with "\n"
// (rare in practice but spec-compliant).
//
// Call Next() repeatedly until io.EOF.
type SSEReader struct {
	br      *bufio.Reader
	buf     strings.Builder // accumulates data fields for current event
	hasData bool
}

// NewSSEReader wraps r for SSE reading.
func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{br: bufio.NewReaderSize(r, 64*1024)}
}

// Next returns the next data payload.
// Returns io.EOF when the stream is closed.
func (s *SSEReader) Next() (string, error) {
	for {
		line, err := s.readLine()
		if err != nil {
			return "", err
		}

		// Blank line = event boundary.
		if line == "" {
			if s.hasData {
				data := s.buf.String()
				s.buf.Reset()
				s.hasData = false
				return data, nil
			}
			continue
		}

		// Comment lines (start with ":")
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Field parsing
		if strings.HasPrefix(line, "data:") {
			val := extractValue(line, "data:")
			if s.hasData {
				s.buf.WriteByte('\n')
			}
			s.buf.WriteString(val)
			s.hasData = true
		}
		// "event:", "id:", "retry:" are ignored for our use case.
	}
}

func (s *SSEReader) readLine() (string, error) {
	line, err := s.br.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			// If we have a partial line, return it; else EOF.
			if strings.TrimSpace(line) != "" {
				return strings.TrimRight(line, "\r\n"), io.EOF
			}
			return "", io.EOF
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// extractValue strips the field prefix and optional single leading space.
func extractValue(line, prefix string) string {
	v := line[len(prefix):]
	if strings.HasPrefix(v, " ") {
		v = v[1:]
	}
	return v
}

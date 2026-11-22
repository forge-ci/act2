package common

import (
	"bytes"
	"io"
)

// LineHandler is a callback function for handling a line
type LineHandler func(line string) bool

type lineWriter struct {
	buffer   bytes.Buffer
	handlers []LineHandler
}

// NewLineWriter creates a new instance of a line writer
func NewLineWriter(handlers ...LineHandler) io.Writer {
	w := new(lineWriter)
	w.handlers = handlers
	return w
}

func (lw *lineWriter) Write(p []byte) (n int, err error) {
	pBuf := bytes.NewBuffer(p)
	written := 0
	for {
		line, err := pBuf.ReadString('\n')
		w, _ := lw.buffer.WriteString(line)
		written += w
		if err == nil {
			lw.handleLine(lw.buffer.String())
			lw.buffer.Reset()
		} else if err == io.EOF {
			break
		} else {
			return written, err
		}
	}

	return written, nil
}

// handleLine hands every consumer (raw log output, workflow commands) the
// line as UTF-8; see ToUTF8. A line is whole, so no character is split here.
func (lw *lineWriter) handleLine(line string) {
	line = ToUTF8(line)
	for _, h := range lw.handlers {
		ok := h(line)
		if !ok {
			break
		}
	}
}

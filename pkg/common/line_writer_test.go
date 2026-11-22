package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLineWriter(t *testing.T) {
	lines := make([]string, 0)
	lineHandler := func(s string) bool {
		lines = append(lines, s)
		return true
	}

	lineWriter := NewLineWriter(lineHandler)

	assert := assert.New(t)
	write := func(s string) {
		n, err := lineWriter.Write([]byte(s))
		assert.NoError(err)
		assert.Equal(len(s), n, s)
	}

	write("hello")
	write(" ")
	write("world!!\nextra")
	write(" line\n and another\nlast")
	write(" line\n")
	write("no newline here...")

	assert.Len(lines, 4)
	assert.Equal("hello world!!\n", lines[0])
	assert.Equal("extra line\n", lines[1])
	assert.Equal(" and another\n", lines[2])
	assert.Equal("last line\n", lines[3])
}

func TestLineWriterDecodesOutputToUTF8(t *testing.T) {
	var lines []string
	lineWriter := NewLineWriter(func(s string) bool {
		lines = append(lines, s)
		return true
	})
	// UTF-8 and ANSI colour pass unchanged; a UTF-8 character split across
	// writes is reassembled, not mis-decoded.
	check := []byte("\x1b[32;1mPASS\x1b[0m \xe2\x9c")
	_, err := lineWriter.Write(check)
	assert.NoError(t, err)
	_, err = lineWriter.Write([]byte("\x93 caf\xc3\xa9\r\n"))
	assert.NoError(t, err)
	// A tool writing in a Windows ANSI code page (cp1252) is decoded, never
	// replaced with U+FFFD; valid UTF-8 in the same line is kept.
	_, err = lineWriter.Write([]byte("caf\xe9 \x93quoted\x94 \x80 \xa0| \xe2\x9c\x93\n"))
	assert.NoError(t, err)
	assert.Equal(t, []string{
		"\x1b[32;1mPASS\x1b[0m ✓ café\r\n",
		"café “quoted” €  | ✓\n",
	}, lines)
}

func TestToUTF8(t *testing.T) {
	assert.Equal(t, "", ToUTF8(""))
	assert.Equal(t, "plain ✓", ToUTF8("plain ✓"))
	assert.Equal(t, "\u0081\u008d\u008f\u0090\u009d", ToUTF8("\x81\x8d\x8f\x90\x9d"))
	assert.Equal(t, "Ÿÿ", ToUTF8("\x9f\xff"))
}

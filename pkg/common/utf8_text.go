package common

import (
	"strings"
	"unicode/utf8"
)

// cp1252High maps Windows code page 1252 bytes 0x80..0x9F; 0xA0..0xFF are
// Latin-1. The five bytes the code page leaves undefined map to their C1
// controls, as the WHATWG windows-1252 decoder does.
var cp1252High = [32]rune{
	0x20ac, 0x81, 0x201a, 0x192, 0x201e, 0x2026, 0x2020, 0x2021,
	0x2c6, 0x2030, 0x160, 0x2039, 0x152, 0x8d, 0x17d, 0x8f,
	0x90, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014,
	0x2dc, 0x2122, 0x161, 0x203a, 0x153, 0x9d, 0x17e, 0x178,
}

// ToUTF8 converts one piece of process output to valid UTF-8 at the point it
// enters act. Valid UTF-8 is kept as written, including ANSI escape
// sequences; a byte that is not part of valid UTF-8 came from a tool writing
// in a Windows ANSI code page and is decoded as code page 1252, rather than
// becoming U+FFFD when the line is later encoded as JSON.
func ToUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/2)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r != utf8.RuneError || size > 1:
			b.WriteString(s[i : i+size])
		case s[i] >= 0x80 && s[i] <= 0x9f:
			b.WriteRune(cp1252High[s[i]-0x80])
		default:
			b.WriteRune(rune(s[i]))
		}
		i += size
	}
	return b.String()
}

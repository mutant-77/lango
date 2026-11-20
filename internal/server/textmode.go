package server

import (
	"strings"

	"lango/internal/pyutf8"
)

func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1C, 0x1D, 0x1E, 0x85, 0x2028, 0x2029:
		return true
	default:
		return false
	}
}

func splitlinesCount(s string) int {
	if len(s) == 0 {
		return 0
	}
	runes := []rune(s)
	n := len(runes)
	bCount := 0
	i := 0
	endsWithBreak := false

	for i < n {
		r := runes[i]
		if r == '\r' && i+1 < n && runes[i+1] == '\n' {
			bCount++
			i += 2
			if i == n {
				endsWithBreak = true
			}
		} else if isLineBreak(r) {
			bCount++
			i++
			if i == n {
				endsWithBreak = true
			}
		} else {
			i++
		}
	}

	if endsWithBreak {
		return bCount
	}
	return bCount + 1
}

// ReadText decodes bytes using pyutf8, converts universal newlines to \n, and counts splitlines.
func ReadText(b []byte) (string, int) {
	s := pyutf8.DecodeReplace(b)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := splitlinesCount(s)
	if lines == 0 {
		lines = 1
	}
	return s, lines
}

// WriteText converts \n to \r\n and returns UTF-8 bytes without BOM.
func WriteText(content string) []byte {
	return []byte(strings.ReplaceAll(content, "\n", "\r\n"))
}

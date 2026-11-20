package pyutf8

import (
	"strings"
	"unicode/utf8"
)

// DecodeReplace decodes UTF-8 bytes using Unicode maximal subpart replacement (matching Python errors="replace").
func DecodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}

	var sb strings.Builder
	sb.Grow(len(b))
	n := len(b)
	i := 0

	for i < n {
		b0 := b[i]
		if b0 <= 0x7F {
			sb.WriteByte(b0)
			i++
			continue
		}

		if b0 >= 0xC2 && b0 <= 0xDF {
			if i+1 < n && b[i+1] >= 0x80 && b[i+1] <= 0xBF {
				r := rune(b0&0x1F)<<6 | rune(b[i+1]&0x3F)
				sb.WriteRune(r)
				i += 2
			} else {
				sb.WriteRune(utf8.RuneError)
				i++
			}
			continue
		}

		if b0 >= 0xE0 && b0 <= 0xEF {
			var b1Min, b1Max byte = 0x80, 0xBF
			if b0 == 0xE0 {
				b1Min = 0xA0
			} else if b0 == 0xED {
				b1Max = 0x9F
			}

			if i+1 < n && b[i+1] >= b1Min && b[i+1] <= b1Max {
				if i+2 < n && b[i+2] >= 0x80 && b[i+2] <= 0xBF {
					r := rune(b0&0x0F)<<12 | rune(b[i+1]&0x3F)<<6 | rune(b[i+2]&0x3F)
					sb.WriteRune(r)
					i += 3
				} else {
					sb.WriteRune(utf8.RuneError)
					i += 2
				}
			} else {
				sb.WriteRune(utf8.RuneError)
				i++
			}
			continue
		}

		if b0 >= 0xF0 && b0 <= 0xF4 {
			var b1Min, b1Max byte = 0x80, 0xBF
			if b0 == 0xF0 {
				b1Min = 0x90
			} else if b0 == 0xF4 {
				b1Max = 0x8F
			}

			if i+1 < n && b[i+1] >= b1Min && b[i+1] <= b1Max {
				if i+2 < n && b[i+2] >= 0x80 && b[i+2] <= 0xBF {
					if i+3 < n && b[i+3] >= 0x80 && b[i+3] <= 0xBF {
						r := rune(b0&0x07)<<18 | rune(b[i+1]&0x3F)<<12 | rune(b[i+2]&0x3F)<<6 | rune(b[i+3]&0x3F)
						sb.WriteRune(r)
						i += 4
					} else {
						sb.WriteRune(utf8.RuneError)
						i += 3
					}
				} else {
					sb.WriteRune(utf8.RuneError)
					i += 2
				}
			} else {
				sb.WriteRune(utf8.RuneError)
				i++
			}
			continue
		}

		sb.WriteRune(utf8.RuneError)
		i++
	}

	return sb.String()
}

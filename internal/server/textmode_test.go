package server

import (
	"bytes"
	"testing"
)

func TestTextMode(t *testing.T) {
	// 1. Universal newlines: \r\n and lone \r -> \n
	raw := []byte("line1\r\nline2\rline3\nline4")
	content, lines := ReadText(raw)
	wantContent := "line1\nline2\nline3\nline4"
	if content != wantContent {
		t.Errorf("content = %q, want %q", content, wantContent)
	}
	if lines != 4 {
		t.Errorf("lines = %d, want 4", lines)
	}

	// 2. BOM kept as U+FEFF
	rawBOM := []byte("\xef\xbb\xbfhola\n")
	contentBOM, linesBOM := ReadText(rawBOM)
	if !bytes.HasPrefix([]byte(contentBOM), []byte("\xef\xbb\xbf")) {
		t.Errorf("BOM should be preserved")
	}
	if linesBOM != 1 {
		t.Errorf("lines = %d, want 1", linesBOM)
	}

	// 3. Empty string
	contentEmpty, linesEmpty := ReadText([]byte{})
	if contentEmpty != "" {
		t.Errorf("expected empty string")
	}
	if linesEmpty != 1 {
		t.Errorf("empty string lines = %d, want 1", linesEmpty)
	}

	// 4. Special Python line breaks: \f, \x1c, \u2028
	rawSeps := []byte("a\fb\x1cc\xe2\x80\xa8d\n")
	_, linesSeps := ReadText(rawSeps)
	if linesSeps != 4 {
		t.Errorf("seps lines = %d, want 4", linesSeps)
	}

	// 5. WriteText: \n -> \r\n, so \r\n becomes \r\r\n
	wt := WriteText("line1\nline2\r\nline3")
	wantWT := "line1\r\nline2\r\r\nline3"
	if string(wt) != wantWT {
		t.Errorf("WriteText = %q, want %q", string(wt), wantWT)
	}
}

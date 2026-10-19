package textclean

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlain(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Walt and Hank<br/>face off.", "Walt and Hank face off."},
		{"<script>alert(1)</script>Title", "alert(1) Title"},
		{"<i>Italic</i> &amp; bold", "Italic & bold"},
		{"&lt;script&gt;alert(1)", "alert(1)"}, // decoded markup is stripped too
		{"a < b and <3", "a < b and <3"},       // a lone < is just text
		{"<!-- hidden -->shown", "shown"},
		{"Para one.\n\nPara two.", "Para one.\n\nPara two."},
		{"lots   of \t spaces", "lots of spaces"},
		{"evil\u202Egnp.exe", "evilgnp.exe"},
		{"bell\x07char", "bell char"},
		{"  trimmed  ", "trimmed"},
	}
	for _, tt := range tests {
		if got := Plain(tt.in, 1000); got != tt.want {
			t.Errorf("Plain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPlainLength(t *testing.T) {
	got := Plain(strings.Repeat("é", 100), 51)
	if len(got) > 51 || !utf8.ValidString(got) {
		t.Errorf("got %d bytes, valid UTF-8: %v", len(got), utf8.ValidString(got))
	}
}

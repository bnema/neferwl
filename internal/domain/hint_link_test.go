package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidHintURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"https", "https://a.b/c?d#e", true},
		{"http port", "http://a.b:8080", true},
		{"mailto", "mailto:x@y.org", true},
		{"empty", "", false},
		{"uppercase scheme", "HTTPS://x", false},
		{"file", "file:///etc/passwd", false},
		{"ftp", "ftp://x", false},
		{"javascript", "javascript:alert(1)", false},
		{"data", "data:text/html,x", false},
		{"leading dash", "-https://x", false},
		{"no host", "https://", false},
		{"port only host", "http://:8080/path", false},
		{"userinfo", "https://u@h.com", false},
		{"mailto query", "mailto:x@y.org?attach=/etc/passwd", false},
		{"mailto two at", "mailto:a@b@c", false},
		{"space", "https://a.b/c d", false},
		{"newline", "https://a.b/\n", false},
		{"nul", "https://a.b/\x00", false},
		{"quote", `https://a.b/"`, false},
		{"backtick", "https://a.b/`", false},
		{"backslash", `https://a.b/\`, false},
		{"non-ascii", "https://exämple.com", false},
		{"too long", "https://a.b/" + strings.Repeat("a", 2049), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidHintURL(tt.in))
		})
	}
}

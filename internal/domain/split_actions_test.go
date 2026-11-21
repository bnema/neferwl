package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitActions(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"a", []string{"a"}},
		{"a; b", []string{"a", "b"}},
		{" a ;b ", []string{"a", "b"}},
		{"a;;b;", []string{"a", "b"}},
		{"", nil},
		{" ; ", nil},
		{"spawn foo --x", []string{"spawn foo --x"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, SplitActions(tt.in))
		})
	}
}

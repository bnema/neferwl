package domain

import (
	"slices"
	"strings"
	"testing"
)

func TestMarkSetWith(t *testing.T) {
	for _, tc := range []struct {
		set  MarkSet
		name string
		want MarkSet
	}{
		{"", "b", "b"},
		{"b", "a", "a b"},
		{"a c", "b", "a b c"},
		{"a b", "a", "a b"},
		{"a", "bad name", "a"},
		{"a", "", "a"},
	} {
		if got := tc.set.With(tc.name); got != tc.want {
			t.Errorf("%q.With(%q) = %q, want %q", tc.set, tc.name, got, tc.want)
		}
	}
}

func TestMarkSetWithout(t *testing.T) {
	for _, tc := range []struct {
		set  MarkSet
		name string
		want MarkSet
	}{
		{"a b", "c", "a b"},
		{"a b c", "b", "a c"},
		{"a", "a", ""},
		{"", "a", ""},
		{"ab", "a", "ab"},
	} {
		if got := tc.set.Without(tc.name); got != tc.want {
			t.Errorf("%q.Without(%q) = %q, want %q", tc.set, tc.name, got, tc.want)
		}
	}
}

func TestMarkSetHas(t *testing.T) {
	for _, tc := range []struct {
		set  MarkSet
		name string
		want bool
	}{
		{"ab", "a", false},
		{"a ab", "ab", true},
		{"", "", false},
		{"a b", "b", true},
	} {
		if got := tc.set.Has(tc.name); got != tc.want {
			t.Errorf("%q.Has(%q) = %v", tc.set, tc.name, got)
		}
	}
}

func TestMarkSetNames(t *testing.T) {
	if got := MarkSet("").Names(); got == nil || len(got) != 0 {
		t.Fatalf("empty Names = %#v", got)
	}
	if got := MarkSet("a b").Names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Names = %#v", got)
	}
	if !MarkSet("").Empty() || MarkSet("a").Empty() {
		t.Fatal("Empty")
	}
}

func TestValidMarkName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"", false},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"a b", false},
		{"a:b", false},
		{"ok_-1", true},
		{"Zé", false},
	} {
		if got := ValidMarkName(tc.name); got != tc.want {
			t.Errorf("ValidMarkName(%q) = %v", tc.name, got)
		}
	}
}

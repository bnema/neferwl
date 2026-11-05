package cli

import "testing"

func TestInjectUsage(t *testing.T) {
	for _, args := range [][]string{
		{"key"}, {"key", "a", "b"}, {"type"}, {"press"}, {"release"},
		{"move", "1"}, {"move", "NaN", "2"}, {"click", "bad"}, {"click", "left", "right"},
		{"swipe", "2", "up"}, {"swipe", "3", "sideways"},
		{"key", "a", "--wait", "bad"}, {"type", "text", "--until", "bad"},
		{"key", "a", "--timeout", "0s"}, {"key", "a", "--unknown", "value"}, {"key", "a", "--timeout"},
	} {
		t.Run(args[0], func(t *testing.T) {
			code, _, _ := execute(t, args...)
			if code != 2 {
				t.Fatalf("%v: exit %d", args, code)
			}
		})
	}
}

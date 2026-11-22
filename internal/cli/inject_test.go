package cli

import (
	"slices"
	"testing"
)

func TestInjectUsage(t *testing.T) {
	for _, args := range [][]string{
		{"key"}, {"key", "a", "b"}, {"type"}, {"press"}, {"release"},
		{"move", "1"}, {"move", "NaN", "2"}, {"click", "bad"}, {"click", "left", "right"},
		{"down", "bad"}, {"up", "left", "right"},
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

func TestPointerDownUpRequest(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want []string
	}{
		{"down", nil, []string{"left"}},
		{"up", nil, []string{"left"}},
		{"down", []string{"right"}, []string{"right"}},
		{"up", []string{"middle"}, []string{"middle"}},
	} {
		req, err := injectRequest(c.name, c.args)
		if err != nil {
			t.Fatalf("%s %v: %v", c.name, c.args, err)
		}
		if req["kind"] != "pointer" || req["cmd"] != c.name || !slices.Equal(req["args"].([]string), c.want) {
			t.Fatalf("%s %v: %v", c.name, c.args, req)
		}
	}
	if _, err := injectRequest("down", []string{"side"}); err == nil {
		t.Fatal("unknown button accepted")
	}
}

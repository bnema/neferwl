package domain

import "testing"

func TestConfigEqualModes(t *testing.T) {
	a := Config{Modes: map[string]map[string]string{"resize": {"h": "set-column-width -1"}}}
	b := Config{Modes: map[string]map[string]string{"resize": {"h": "set-column-width -1"}}}
	if !a.Equal(b) {
		t.Fatal("equal modes differ")
	}
	b.Modes["resize"]["h"] = "set-column-width -2"
	if a.Equal(b) {
		t.Fatal("changed mode bind compares equal")
	}
	if a.Equal(Config{}) {
		t.Fatal("nil modes compare equal to declared modes")
	}
}

func TestLookupModeCommand(t *testing.T) {
	if spec, ok := LookupCommand("mode resize"); !ok || spec.Kind != CommandActionArg {
		t.Fatalf("mode resize: %+v %v", spec, ok)
	}
}

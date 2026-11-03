package domain

import "testing"

func TestOutputInputsEquality(t *testing.T) {
	a := Config{Outputs: []OutputConfig{{Name: "tv", Inputs: []string{"Mouse*"}}}}
	b := Config{Outputs: []OutputConfig{{Name: "tv", Inputs: []string{"Mouse*"}}}}
	if !a.Equal(b) {
		t.Fatal("equal globs at different addresses differ")
	}
	b.Outputs[0].Inputs[0] = "Tablet*"
	if a.Equal(b) {
		t.Fatal("changed glob missed")
	}
	b.Outputs[0].Inputs = []string{"Mouse*", "Tablet*"}
	if a.Equal(b) {
		t.Fatal("added glob missed")
	}
}

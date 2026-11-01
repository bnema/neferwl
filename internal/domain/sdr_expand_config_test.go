package domain

import "testing"

func TestExpansionConfigEquality(t *testing.T) {
	intensity := 0.5
	a := Config{Rules: []WindowRule{{SDRExpandIntensity: &intensity}}}
	same := 0.5
	b := Config{Rules: []WindowRule{{SDRExpandIntensity: &same}}}
	if !a.Equal(b) {
		t.Fatal("equal values at different addresses differ")
	}
	same = 0.7
	if a.Equal(b) {
		t.Fatal("intensity change missed")
	}
	for _, edit := range []func(*WindowRule){
		func(r *WindowRule) { v := SDRExpandOn; r.SDRExpand = &v },
		func(r *WindowRule) { v := 600.0; r.SDRExpandPeak = &v },
		func(r *WindowRule) { v := SDRDecodeSRGB; r.SDRDecode = &v },
	} {
		a := Config{Rules: []WindowRule{{}}}
		b := Config{Rules: []WindowRule{{}}}
		edit(&b.Rules[0])
		if a.Equal(b) {
			t.Fatal("rule change missed")
		}
	}
	for _, edit := range []func(*OutputConfig){
		func(o *OutputConfig) { o.SDRExpand = SDRExpandOff }, func(o *OutputConfig) { o.SDRExpandIntensity = 0.5 }, func(o *OutputConfig) { o.SDRExpandPeak = 600 }, func(o *OutputConfig) { o.SDRDecode = SDRDecodeSRGB },
	} {
		a := Config{Outputs: []OutputConfig{{}}}
		b := Config{Outputs: []OutputConfig{{}}}
		edit(&b.Outputs[0])
		if a.Equal(b) {
			t.Fatal("output change missed")
		}
	}
}

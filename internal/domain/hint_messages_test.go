package domain

import "testing"

func TestHintSourceString(t *testing.T) {
	for s, want := range map[HintSource]string{0: "", HintSourceATSPI: "atspi", HintSourceOCR: "ocr", HintSourceDetector: "detector"} {
		if s.String() != want {
			t.Fatal(s)
		}
	}
}

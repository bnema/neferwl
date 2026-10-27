package domain

// HintSource identifies the source of a target; config determines chain order.
type HintSource uint8

const (
	HintSourceATSPI HintSource = iota + 1
	HintSourceOCR
	HintSourceDetector
)

func (s HintSource) String() string {
	switch s {
	case HintSourceATSPI:
		return "atspi"
	case HintSourceOCR:
		return "ocr"
	case HintSourceDetector:
		return "detector"
	}
	return ""
}

type HintKind uint8

const (
	HintLink HintKind = iota
	HintButton
)

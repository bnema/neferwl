package config

import (
	"fmt"
	"strconv"

	"github.com/bnema/neferwl/internal/domain"
)

func expansionNumber(field, s string) (float64, error) {
	n, err := strconv.ParseFloat(s, 64)
	if field == "sdr-expand-intensity" {
		if err != nil || !(n >= 0 && n <= 1) {
			return 0, fmt.Errorf("must be between 0 and 1")
		}
	} else if err != nil || !(n == 0 || n >= 200 && n <= 10000) {
		return 0, fmt.Errorf("must be 0 (automatic) or between 200 and 10000 nits")
	}
	return n, nil
}

func setOutputExpansion(o *domain.OutputConfig, field, s string) error {
	switch field {
	case "sdr-expand":
		v, ok := domain.ParseSDRExpandMode(s)
		if !ok {
			return fmt.Errorf("must be off, auto or always")
		}
		o.SDRExpand = v
	case "sdr-decode":
		v, ok := domain.ParseSDRDecode(s)
		if !ok {
			return fmt.Errorf("must be gamma22 or srgb")
		}
		o.SDRDecode = v
	case "sdr-expand-intensity", "sdr-expand-peak":
		v, err := expansionNumber(field, s)
		if err != nil {
			return err
		}
		if field == "sdr-expand-intensity" {
			o.SDRExpandIntensity = v
		} else {
			o.SDRExpandPeak = v
		}
	}
	return nil
}

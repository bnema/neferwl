package domain

import "math"

// RefreshMilli converts Hz to the positive signed-32-bit mHz range carried by
// output protocols. Validation happens after rounding, before integer conversion.
func RefreshMilli(hz float64) (int, bool) {
	value := math.Round(hz * 1000)
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 1 || value > math.MaxInt32 {
		return 0, false
	}
	return int(value), true
}

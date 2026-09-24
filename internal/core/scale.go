package core

import "math"

// Scales are in 1/120 steps, the wp_fractional_scale_v1 unit.
const scaleDen = 120

// CleanScales lists scales between 1 and 3 where the output divides into whole
// logical pixels, so window edges land on physical pixels. Scales below 1.1
// are skipped: 1.067 on 5120 wide outputs is too close to 1 to be useful.
func CleanScales(width, height int) []float64 {
	out := []float64{1}
	for n := scaleDen + 1; n <= 3*scaleDen; n++ {
		if width*scaleDen%n != 0 || height*scaleDen%n != 0 {
			continue
		}
		if s := float64(n) / scaleDen; s >= 1.1 {
			out = append(out, s)
		}
	}
	return out
}

// StepScale returns the next clean scale above (dir 1) or below (dir -1) cur,
// or cur at the ends.
func StepScale(width, height int, cur float64, dir int) float64 {
	list := CleanScales(width, height)
	if dir > 0 {
		for _, s := range list {
			if s > cur+1e-6 {
				return s
			}
		}
		return cur
	}
	for i := len(list) - 1; i >= 0; i-- {
		if list[i] < cur-1e-6 {
			return list[i]
		}
	}
	return cur
}

// SnapScale rounds to the nearest 1/120 and clamps to [1, 4].
func SnapScale(s float64) float64 {
	if !(s >= 1) {
		return 1
	}
	return math.Round(min(s, 4)*scaleDen) / scaleDen
}

// logical converts a physical size to logical pixels.
func logical(v int, scale float64) int { return int(math.Round(float64(v) / scale)) }

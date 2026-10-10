package config

import (
	"fmt"
	"math"
	"strconv"

	"github.com/bnema/kvconf"
)

// ScaleStore writes output.<name>.scale into the config file, preserving
// unrelated lines and comments.
type ScaleStore struct{ Path string }

// SaveOutputScale updates output.<name>.scale in the config file. The file is
// replaced atomically; a symlinked config is written through to its target.
// A new config may hold commands: only its owner reads it (0600).
func (s ScaleStore) SaveOutputScale(output string, scale float64) error {
	value := formatScale(scale)
	return kvconf.SetFile(s.Path, map[string]*string{"output." + output + ".scale": &value}, 0o600)
}

// formatScale writes scales as the config expects them: a decimal when it is
// exact (1.25), else a fraction (4/3).
func formatScale(s float64) string {
	n, d := int(math.Round(s*120)), 120
	g := gcd(n, d)
	n, d = n/g, d/g
	den := d
	for den%2 == 0 {
		den /= 2
	}
	for den%5 == 0 {
		den /= 5
	}
	if den == 1 {
		return strconv.FormatFloat(float64(n)/float64(d), 'f', -1, 64)
	}
	return fmt.Sprintf("%d/%d", n, d)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

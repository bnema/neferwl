package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveOutputScale(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		scale          float64
	}{
		{"missing file", "", "output.DP-2.scale = 1.5\n", 1.5},
		{"append", "# my config\nborder.width = 3", "# my config\nborder.width = 3\noutput.DP-2.scale = 1.5\n", 1.5},
		{"replace in place", "output.DP-2.scale = 2 # big\n# end\n", "output.DP-2.scale = 1.25 # big\n# end\n", 1.25},
		{"keeps CRLF", "output.DP-2.scale = 2\r\nborder.width = 3\r\n", "output.DP-2.scale = 1.5\r\nborder.width = 3\r\n", 1.5},
		{"comment with = and #", "output.DP-2.scale = 2 # was a=b #1\n", "output.DP-2.scale = 1.5 # was a=b #1\n", 1.5},
		{"keeps spacing", "output.DP-2.scale=2\n", "output.DP-2.scale=4/3\n", 4.0 / 3},
		{"last line wins", "output.DP-2.scale = 2\noutput.DP-2.scale = 3\n", "output.DP-2.scale = 2\noutput.DP-2.scale = 5/3\n", 5.0 / 3},
		{"ignores comments and other outputs", "# output.DP-2.scale = 2\noutput.DP-1.scale = 2\n", "# output.DP-2.scale = 2\noutput.DP-1.scale = 2\noutput.DP-2.scale = 1\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "neferwl", "config")
			if tc.in != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.in), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := (ScaleStore{Path: path}).SaveOutputScale("DP-2", tc.scale); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
			// The written value parses back to the same scale.
			cfg, _, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range cfg.Outputs {
				if o.Name == "DP-2" && o.Scale != tc.scale {
					t.Fatalf("parsed %v, want %v", o.Scale, tc.scale)
				}
			}
			// Existing files keep their mode; new ones are private.
			{
				if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
					t.Fatalf("mode %v", info.Mode().Perm())
				}
			}
		})
	}
}

// A symlinked config (dotfiles) stays a symlink; its target gets the value.
func TestSaveOutputScaleSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-config")
	if err := os.WriteFile(target, []byte("border.width = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := (ScaleStore{Path: link}).SaveOutputScale("DP-2", 2); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "border.width = 3\noutput.DP-2.scale = 2\n" {
		t.Fatalf("%q", got)
	}
	// A dangling link is an error and stays in place.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := (ScaleStore{Path: link}).SaveOutputScale("DP-2", 2); err == nil {
		t.Fatal("dangling link saved")
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("dangling symlink replaced", err)
	}
}

// Every clean scale is written in a form the parser reads back exactly.
func TestFormatScaleRoundTrip(t *testing.T) {
	for n := 120; n <= 480; n++ {
		s := float64(n) / 120
		got, err := parseScale(formatScale(s))
		if err != nil || math.Round(got*120) != float64(n) {
			t.Fatalf("%d/120: %q parsed as %v, %v", n, formatScale(s), got, err)
		}
	}
}

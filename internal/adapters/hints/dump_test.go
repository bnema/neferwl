package hints

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/neferwl/internal/ports"
)

// An existing dump directory is made private, and a symlink at a file name is
// replaced instead of followed.
func TestWriteDumpPrivate(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "hints")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))
	victim := filepath.Join(base, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("keep"), 0o644))
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	prefix := filepath.Join(dir, when.Format("20060102-150405.000")+"-o")
	require.NoError(t, os.Symlink(victim, prefix+".json"))
	pix := make([]byte, 4*4*4)
	j := &dumpJob{when: when, req: ports.HintScanRequest{Output: "o", Width: 4, Height: 4, Scale: 1}, pix: pix, stride: 16, m: &mapping{}}
	require.NoError(t, writeDump(dir, j))

	fi, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	require.Equal(t, "keep", string(got))
	li, err := os.Lstat(prefix + ".json")
	require.NoError(t, err)
	require.True(t, li.Mode().IsRegular())
	require.Equal(t, os.FileMode(0o600), li.Mode().Perm())
}

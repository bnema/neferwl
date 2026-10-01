package workspaceid

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfiguredWorkspaceIDIsItsNameWhateverTheCoreID(t *testing.T) {
	ids := WithPrefix("0a1b2c3d")
	require.Equal(t, "name:dev", ids.ID(7, "dev"))
	require.Equal(t, "name:dev", ids.ID(99, "dev"), "stable across launches and core IDs")
	require.NotEqual(t, ids.ID(7, "dev"), ids.ID(7, "web"))
}

func TestNumberedWorkspaceIDIsLaunchPrefixAndCoreID(t *testing.T) {
	ids := WithPrefix("0a1b2c3d")
	require.Equal(t, "0a1b2c3d-7", ids.ID(7, ""))
	require.NotEqual(t, ids.ID(7, ""), ids.ID(8, ""))
}

func TestLaunchPrefixIsEightLowercaseHexAndDiffersPerLaunch(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-1$`)
	a, b := New().ID(1, ""), New().ID(1, "")
	require.Regexp(t, re, a)
	require.Regexp(t, re, b)
	require.NotEqual(t, a, b, "two launches draw two prefixes")
}

package ports

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPresentationSnapshotReleaseUnderflow(t *testing.T) {
	returned := make(chan *PresentationSnapshot, 1)
	snapshot := &PresentationSnapshot{}
	snapshot.Init(returned)
	snapshot.Release()
	require.Same(t, snapshot, <-returned)
	require.PanicsWithValue(t, "ports.PresentationSnapshot.Release: reference count underflow", snapshot.Release)
	require.Empty(t, returned, "underflow must not return a snapshot")
}

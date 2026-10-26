package core

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core/motion"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceSlideDragAndStop(t *testing.T) {
	w := &Workspace{}
	w.View, w.stashAt = 100, 2
	require.Equal(t, float64(100), w.ColumnsPosition())
	require.Equal(t, float64(2), w.StashPosition())
	require.False(t, w.SlidesBusy())
	require.False(t, w.SlidesRunning())

	w.DragColumns(137.5)
	w.DragStash(1.25)
	require.Equal(t, float64(137.5), w.ColumnsPosition())
	require.Equal(t, float64(1.25), w.StashPosition())
	require.True(t, w.SlidesBusy())
	require.False(t, w.SlidesRunning())
	// Disabling animations must leave the fingers' offsets alone.
	w.StopLandings()
	require.Equal(t, float64(137.5), w.ColumnsPosition())
	require.Equal(t, float64(1.25), w.StashPosition())
	// Rebasing after focus changes preserves the drawn position.
	w.View, w.stashAt = 150, 1
	w.DragColumns(137.5)
	w.DragStash(1.25)
	require.Equal(t, -12.5, w.view.Off)
	require.Equal(t, 0.25, w.stashView.Off)
	w.StopColumns()
	require.Equal(t, float64(150), w.ColumnsPosition())
	require.Equal(t, float64(1.25), w.StashPosition())
	w.StopStash()
	require.Equal(t, float64(1), w.StashPosition())
	require.False(t, w.SlidesBusy())
}

func TestWorkspaceSlideLandingSampleAndStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		w := &Workspace{}
		w.View, w.stashAt = 100, 2
		w.DragColumns(140)
		w.DragStash(1.25)
		columns := motion.NewMotion(motion.ViewSpring(40, 75), now, 2)
		stash := motion.NewMotion(motion.WorkspaceSpring(-0.75, -1), now, 2)
		w.LandColumns(columns)
		w.LandStash(stash)
		require.True(t, w.SlidesRunning())
		require.True(t, w.SlidesBusy())
		time.Sleep(20 * time.Millisecond)
		sampled := time.Now()
		beforeColumns, beforeStash := w.view, w.stashView
		pos, velocity := w.SampleColumns(sampled)
		_, wantVelocity := columns.SampleAt(sampled)
		require.Equal(t, float64(140), pos)
		require.Equal(t, wantVelocity, velocity)
		pos, velocity = w.SampleStash(sampled)
		_, wantVelocity = stash.SampleAt(sampled)
		require.Equal(t, 1.25, pos)
		require.Equal(t, wantVelocity, velocity)
		require.Equal(t, beforeColumns, w.view)
		require.Equal(t, beforeStash, w.stashView)

		wantColumns, wantStash := beforeColumns, beforeStash
		wantColumns.Step(sampled)
		wantStash.Step(sampled)
		w.StepSlides(sampled)
		require.Equal(t, wantColumns, w.view)
		require.Equal(t, wantStash, w.stashView)
		time.Sleep(10 * time.Second)
		w.StepSlides(time.Now())
		require.False(t, w.SlidesRunning())
		require.False(t, w.SlidesBusy())
		require.Equal(t, float64(100), w.ColumnsPosition())
		require.Equal(t, float64(2), w.StashPosition())
	})
}

func TestWorkspaceSlideDragTakesOverLanding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		w := &Workspace{}
		w.View, w.stashAt = 20, 1
		w.DragColumns(45)
		w.DragStash(1.5)
		w.LandColumns(motion.NewMotion(motion.ViewSpring(25, 5), now, 1))
		w.LandStash(motion.NewMotion(motion.WorkspaceSpring(0.5, 1), now, 1))
		require.Equal(t, float64(45), w.BeginColumnsDrag())
		require.True(t, w.SlidesRunning()) // Stash still lands.
		require.Equal(t, 1.5, w.BeginStashDrag())
		require.False(t, w.SlidesRunning())
		require.True(t, w.SlidesBusy())
		time.Sleep(time.Second)
		w.StepSlides(time.Now())
		require.Equal(t, float64(45), w.ColumnsPosition())
		require.Equal(t, 1.5, w.StashPosition())
		pos, velocity := w.SampleColumns(time.Now())
		require.Equal(t, float64(45), pos)
		require.Zero(t, velocity)
		pos, velocity = w.SampleStash(time.Now())
		require.Equal(t, 1.5, pos)
		require.Zero(t, velocity)

		// Stop only the running stash, preserving a column drag.
		w.LandStash(motion.NewMotion(motion.WorkspaceSpring(0.5, 0), time.Now(), 1))
		w.StopLandings()
		require.Equal(t, float64(45), w.ColumnsPosition())
		require.Equal(t, float64(1), w.StashPosition())
		w.LandColumns(motion.NewMotion(motion.ViewSpring(25, 0), time.Now(), 1))
		w.StopLandings()
		require.False(t, w.SlidesBusy())
	})
}

package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestRouteIDs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scene domain.Scene
		want  []WindowID
	}{
		{"hidden", domain.Scene{Windows: []domain.SceneWindow{{ID: 2, Hidden: true}}}, []WindowID{2}},
		{"popup", domain.Scene{Windows: []domain.SceneWindow{{ID: 3}}}, []WindowID{3}},
		{"layer", domain.Scene{Layers: []domain.SceneLayer{{ID: 4}}}, []WindowID{4}},
		{"capture", domain.Scene{CaptureScene: &domain.Scene{Windows: []domain.SceneWindow{{ID: 5}}}}, []WindowID{5}},
		{"duplicates sorted", domain.Scene{Windows: []domain.SceneWindow{{ID: 3}, {ID: 1}}, Layers: []domain.SceneLayer{{ID: 3}}, CaptureScene: &domain.Scene{Windows: []domain.SceneWindow{{ID: 1}, {ID: 2}}}}, []WindowID{1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, routeIDs(nil, tc.scene)) })
	}
}

func unwrapSecurity(cmd ports.ClientCommand) ports.ClientCommand {
	if s, ok := cmd.(ports.SecurityCommand); ok {
		return s.Command
	}
	return cmd
}

func routeCommands(c *Core, commands chan ports.ClientCommand) []domain.ContentRoute {
	var routes []domain.ContentRoute
	for len(commands) > 0 {
		if cmd, ok := unwrapSecurity(<-commands).(ports.SetContentRoutes); ok {
			routes = append(routes, cmd.Routes...)
		}
	}
	for _, cmd := range takeAll(&c.out) {
		if cmd, ok := unwrapSecurity(cmd).(ports.SetContentRoutes); ok {
			routes = append(routes, cmd.Routes...)
		}
	}
	return routes
}

func TestSyncRoutesOnlyChangesAndGainGenerations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := publishRig(t, 2)
		commands := make(chan ports.ClientCommand, 128)
		c.ch.Commands = commands
		scenes := []domain.Scene{{Output: "A", Windows: []domain.SceneWindow{{ID: 1}, {ID: 2}}}, {Output: "B"}}
		require.NoError(t, c.syncRoutes(context.Background(), scenes))
		require.Equal(t, []domain.ContentRoute{{Output: "A", Gen: 1, Windows: []WindowID{1, 2}}}, routeCommands(c, commands))
		require.Equal(t, uint64(1), scenes[0].Routes)
		require.NoError(t, c.syncRoutes(context.Background(), scenes))
		require.Empty(t, routeCommands(c, commands))
		scenes[0].Windows = []domain.SceneWindow{{ID: 2}}
		scenes[1].Windows = []domain.SceneWindow{{ID: 1}}
		require.NoError(t, c.syncRoutes(context.Background(), scenes))
		require.Equal(t, []domain.ContentRoute{{Output: "A", Gen: 1, Windows: []WindowID{2}}, {Output: "B", Gen: 1, Windows: []WindowID{1}}}, routeCommands(c, commands))
		scenes[0].Windows = nil
		require.NoError(t, c.syncRoutes(context.Background(), scenes))
		require.Equal(t, []domain.ContentRoute{{Output: "A", Gen: 1, Windows: []WindowID{}}}, routeCommands(c, commands))
	})
}

func TestPublishRoutesStampLastAndRemainStable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := publishRig(t, 1)
		commands := make(chan ports.ClientCommand, 128)
		c.ch.Commands = commands
		require.NoError(t, c.publish(context.Background()))
		scenes := <-c.ch.Scenes
		routes := routeCommands(c, commands)
		require.Len(t, routes, 1)
		require.Len(t, routes[0].Windows, 6)
		require.Equal(t, uint64(1), scenes[0].Routes)
		require.Equal(t, scenes[0].Routes, c.screens[0].last.Routes)
		require.NoError(t, c.publish(context.Background()))
		<-c.ch.Scenes
		require.Empty(t, routeCommands(c, commands))
	})
}

func TestRoutesStableAcrossWorkspaceAndOverview(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := publishRig(t, 1)
		commands := make(chan ports.ClientCommand, 128)
		c.ch.Commands = commands
		require.NoError(t, c.publish(context.Background()))
		<-c.ch.Scenes
		routeCommands(c, commands)
		for _, action := range []Action{ActionToggleOverview, ActionToggleOverview, ActionFocusWorkspaceNext} {
			require.NoError(t, c.runBind(context.Background(), action))
			require.NoError(t, c.publish(context.Background()))
			<-c.ch.Scenes
			require.Empty(t, routeCommands(c, commands), string(action))
		}
	})
}

func TestProtectedPublishRoutesLockSurface(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, commands := hiddenCaptureCommands(t)
		state := domain.SecurityState{Generation: 1, Protected: true}
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState { return state })
		c.opts.Security = gate
		c.security = state
		c.applyLockChanged(ports.SessionLockChanged{State: state, Surfaces: []domain.LockSurfacePlacement{{ID: 100, Output: "A", Width: 300, Height: 200}}})
		require.NoError(t, c.publishProtected(context.Background()))
		scenes := <-c.ch.Scenes
		routes := routeCommands(c, commands)
		require.Equal(t, []domain.ContentRoute{{Output: "A", Gen: 1, Windows: []WindowID{100}}}, routes)
		require.Equal(t, uint64(1), scenes[0].Routes)
	})
}

func TestRoutesSentAgainForOutputAdoptingPlaceholder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := publishRig(t, 1)
		commands := make(chan ports.ClientCommand, 128)
		c.ch.Commands = commands
		require.NoError(t, c.publish(context.Background()))
		first := routeCommands(c, commands)
		require.Len(t, first, 1)
		require.Equal(t, "A", first[0].Output)
		<-c.ch.Scenes

		c.removeScreen("A")
		c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200})
		require.NoError(t, c.publish(context.Background()))
		second := routeCommands(c, commands)
		require.Len(t, second, 1)
		require.Equal(t, "B", second[0].Output)
		require.Equal(t, first[0].Windows, second[0].Windows)
		require.Greater(t, second[0].Gen, first[0].Gen)
		require.Equal(t, second[0].Gen, (<-c.ch.Scenes)[0].Routes)
	})
}

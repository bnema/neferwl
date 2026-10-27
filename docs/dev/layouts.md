# Layouts

A layout is one file plus one entry in `layoutTable`
(`internal/core/layout/layout_registry.go`) and the places that name config
values. `layout.Lanes` (embedded in `workspace.Workspace`) owns the columns and
calls the layout on its owner goroutine; layouts take no locks and use no ports.

## Add a layout

1. Create `internal/core/layout/layout_<name>.go`. It holds the config value
   (`const OverflowName Overflow = "name"`), a type embedding `layout.Base`,
   and the overrides it needs. A layout with no override is the scroll layout.
2. Add `nameLayout{}` to `layoutTable` in `layout_registry.go`. It is the only
   list: `layout.Lookup` scans it by `Name()` (no map: it runs on every frame
   path), core validation (`invalid overflow`) accepts what it resolves,
   `layout.LayoutNames` exports it, and the test tables derive from it.
3. Name the config value where core cannot be imported:
   - `setLayoutRule` in `internal/adapters/config/config.go` (turns a bad value
     into a warning and keeps loading);
   - the `LayoutRules` and `WorkspaceState` comments in `internal/ports`;
   - `README.md` and the `layout.overflow` rows of `docs/config.md`;
   - `examples/config`.
   `TestConfigAcceptsEveryCoreLayout` (`internal/app`) fails until the config
   adapter accepts the new name.
4. Run the conformance table (below). It covers every layout in `layoutTable`.

Nothing else changes: not the interfaces, not Workspace, not the callers.
`internal/core/workspace/layout_extension_test.go` shows it for three test-only
layouts (a geometry override, a width override, and one with its own state and a
trait override); a layout needing a new concept adds a trait with a neutral
default.

## Interfaces

`layout_iface.go` composes small interfaces, each with a Mockery entry in
`.mockery.yml` (generated into `internal/core/layout/layoutmocks`). Read-only
methods get a `layout.View`: a snapshot of columns, focus, view offset,
capacity, usable and output rects, gap, fullscreen ID, layout state and the
resolved layout. `Lanes.Snapshot` builds it. Its slices alias `Lanes` storage:
never retain or write them.

| Group | Methods | Neutral default (`layout.Base`) |
|---|---|---|
| `Traits` | `Name`, `Axes`, `Scrolls`, `EqualCells`, `MaximizeHides`, `PinsFullscreen`, `ResetsMaximize`, `OverridesWidth` | scroll: horizontal content axis, scrolls, nothing else (`OverridesWidth` false) |
| `Shape` | `Bands`, `LaneGestures`, `Spiral`, `LaneRow` | all false |
| `Geometry` | `ColumnRects`, `ColumnWidth`, `ScrollTarget`, `SnapPoints`, `SnapFocus` | the scroll row |
| `LaneSet` | `LaneOf`, `LaneBounds`, `LaneCount`, `LaneNeighbor` | one lane, no neighbor |
| `Navigator` | `FocusStep`, `LaneStep` | next column; no lane step (`-1` = none) |
| `Placer` | `SpawnAt`, `CoveredSpawnAt`, `Full`, `MoveAllowed`, `MoveLane`, `AdoptFloat`, `Isolate` | after the focus; never full; no lane moves |
| `Sizer` | `Grow`, `Shrink`, `ToggleFullWidth` | cells of the row |
| `Overview` | `PreviewTiles`, `PreviewLanes` | the scroll row |
| `Layout` | all of the above plus `NewState` | nil (stateless) |

Each trait or hook names one concept. Generic code asks for the concept it needs
(`PinsFullscreen`, `Bands`, `Spiral`, ...) and never infers a layout from a
combination of others. If the code needs a concept that does not exist, add a
trait with a neutral default instead of testing `EqualCells() && !MaximizeHides()`.

Base methods that need another layout method call it through `v.Layout`, not
the base receiver: an embedding layout's override is honored
(`Base.ColumnRects` calls `v.Layout.ColumnWidth`, `PreviewTiles` calls
`v.Layout.ColumnRects`). Do the same in your layout. A loop over columns calls
`v.WithFacts()` once first, so each `ColumnWidth` call reuses the
workspace-wide width facts instead of recomputing them.

A layout that overrides `ColumnWidth` returns true from `OverridesWidth`. Loops
over columns (`Base.ColumnRects`) then call its `ColumnWidth` per column; for
the others they compute the base width directly, without a dispatch per column.
`TestOverridesWidthMatchesColumnWidth` fails for a layout that returns false but
sizes columns differently from the base.

`Lanes` caches its resolved layout (`lcache`), so `Engine()` does not scan
`layoutTable` on each call. `Lanes.SetOverflow` is the one writer of `Overflow`
in production code: it updates the cache. `Engine()` re-resolves with
`resolveLayout(w.Overflow)` and refills the cache when it is empty or was
resolved for another value (a zero `Lanes`, a literal in a test, a direct write
to the field), so the two never disagree. `Lanes.Override` still wins over both.

## State and carry

Stateless layouts return nil from `NewState`. Otherwise `Lanes` builds the state
lazily on first use, and a layout switch drops it (`Lanes.ResetLayoutState`).
The `State` hooks run after `Lanes` changed `Columns`:

- `Inserted(w, at, kind, carry) unconsumed`: `kind` is `InsertDefault`,
  `InsertNewLane` or `InsertJoinDisplaced`; `carry` is what `Removed` returned
  elsewhere. It returns the carry it did not consume (nil when it took it).
- `Removed(at, col) carry`: whatever should travel with the column.
- `Settle(w)`: before `scroll()` measures; refresh keyed data, drop stale spans.

`Column.Key` is the workspace-local column identity; state keys per-column data
by it. `Column.Carry` is opaque (`any`): only the state that produced it reads
it. What `Inserted` consumes is cleared from the column; what it returns stays
on the stored column, and a stateless workspace keeps all of it. The next
`takeColumn` passes it on when its own `Removed` returns nil, so a column hops
through a scroll or fixed workspace and gets its cascade neighbors back. A
layout switch drops it with the state. Cascade keeps band starts by position
and push history by key (`cascadeState`).

On a layout switch `Monitor.applyNamed` selects the new layout with
`SetOverflow`, clears maximization when the layout asks (`ResetsMaximize`),
calls `ResetLayoutState`, then adjusts the view axis and capacity. Keep this
order.

## Tests

`internal/core/workspace/layout_conformance_*_test.go` iterate
`conformanceLayouts()`: `layoutTable` plus `testLayouts`, which test files extend
from `init` (production never mutates the table). `conformanceWorkspace` builds
the one fixture: capacities 1-4, empty/single/many columns, maximized and
fullscreen variants; each invariant lives in one file. `layout_golden_test.go`
pins reviewed rects per layout. `layout_extension_test.go` defines its test
layouts in that one file and injects them through `Lanes.Override`.
`layout_dispatch_test.go` checks Workspace results through the generated
composite mock (`layoutmocks.NewMockLayout`): expectations are set on it
directly, driving production code (gestures, spawn, overview), and the narrow
mocks serve tests that need one interface (`layoutmocks.NewMockState` for
carry). Never handwrite doubles. After an interface change run `make mocks` and
commit the generated files.

Tests of the layout engine itself, which reach unexported lane state (band
starts, cell widths, the layout cache), live beside it in
`internal/core/layout` (`insert_test.go`, `cascade_state_test.go`,
`lane_width_test.go`, `cache_test.go`).

Hot paths: resolve the layout and build the `layout.View` once per call, not per
column (`internal/core/workspace/layout_bench_test.go` benchmarks
`BenchmarkLayoutColumnRects`, `BenchmarkLayoutPreviewTiles` and
`BenchmarkLayoutBorderAt` at 12 columns).

Required: `CGO_ENABLED=0 go build ./...`, `make check`, `make race`,
`make mocks-check`, `staticcheck ./...`, `gofmt -l internal/core` (empty) and
`CGO_ENABLED=1 go test -race -count=1 -run Alloc ./internal/core/`.

For end-to-end testing follow [headless.md](headless.md): a throwaway config per
layout, several windows, focus/move on both axes, grow/shrink, maximize,
overview and swipe, with `WAYLAND_DISPLAY` isolated and `NEFERWL_STATE` and
`NEFERWL_SLOT` unset.

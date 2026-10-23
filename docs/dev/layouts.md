# Layouts

A layout is one file plus one entry in `layoutTable` (`layout_registry.go`) and the
places that name config values. Workspace owns the windows and calls the layout on its
owner goroutine; layouts take no locks and use no ports.

## Add a layout

1. Create `internal/core/layout_<name>.go`. It holds the config value
   (`const OverflowName Overflow = "name"`), a type embedding `columnsBase`,
   and the overrides it needs. A layout with no override is the scroll layout.
2. Add `nameLayout{}` to `layoutTable` in `layout_registry.go`. It is the only
   list: `layoutFor` scans it by `name()` (no map: it runs on every frame path),
   core validation (`invalid overflow`) accepts what it resolves, `LayoutNames`
   exports it, and the test tables derive from it.
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
`layout_extension_test.go` shows it for three test-only layouts (a geometry
override, a width override, and one with its own state and a trait override);
a layout needing a new concept adds a trait with a neutral default.

## Interfaces

`layout_iface.go` composes small interfaces, each with a Mockery entry. Read-only
methods get a `layoutView`: a snapshot of columns, focus, view offset, capacity,
usable and output rects, gap, fullscreen ID, layout state and the resolved
layout. Its slices alias Workspace storage: never retain or write them.

| Group | Methods | Neutral default (`columnsBase`) |
|---|---|---|
| traits | `name`, `axes`, `scrolls`, `equalCells`, `maximizeHides`, `pinsFullscreen`, `resetsMaximize`, `overridesWidth` | scroll: horizontal content axis, scrolls, nothing else (`overridesWidth` false) |
| shape | `bands`, `laneGestures`, `spiral`, `laneRow` | all false |
| geometry | `columnRects`, `columnWidth`, `scrollTarget`, `snapPoints`, `snapFocus` | the scroll row |
| lanes | `laneOf`, `laneBounds`, `laneCount`, `laneNeighbor` | one lane, no neighbor |
| navigator | `focusStep`, `laneStep` | next column; no lane step (`-1` = none) |
| placer | `spawnAt`, `coveredSpawnAt`, `full`, `moveAllowed`, `moveLane`, `isolate` | after the focus; never full; no lane moves |
| sizer | `grow`, `shrink`, `toggleFullWidth` | cells of the row |
| overview | `previewTiles`, `previewLanes` | the scroll row |
| state | `newState` | nil (stateless) |

Each trait or hook names one concept. Generic code asks for the concept it needs
(`pinsFullscreen`, `bands`, `spiral`, ...) and never infers a layout from a
combination of others. If the code needs a concept that does not exist, add a
trait with a neutral default instead of testing `equalCells() && !maximizeHides()`.

Base methods that need another layout method call it through `v.layout`, not
the base receiver: an embedding layout's override is honored
(`columnsBase.columnRects` calls `v.layout.columnWidth`, `previewTiles` calls
`v.layout.columnRects`). Do the same in your layout. A loop over columns calls
`v.withFacts(&w)` once first, so each `columnWidth` call reuses the
workspace-wide width facts instead of recomputing them.

A layout that overrides `columnWidth` returns true from `overridesWidth`. Loops
over columns (`columnsBase.columnRects`) then call its `columnWidth` per column;
for the others they call the base width directly, without a dispatch per column.
`TestOverridesWidthMatchesColumnWidth` fails for a layout that returns false but
sizes columns differently from the base.

`Workspace` caches its resolved layout (`lcache`), so `layout()` and `lview()`
do not scan `layoutTable`. `setOverflow` is the one writer of `Overflow` in
production code: it updates the cache. `layout()` re-resolves with
`layoutFor(w.Overflow)` and refills the cache when it is empty or was resolved
for another value (a zero Workspace, a literal in a test, a direct write to the
field), so the two never disagree. `Workspace.lay` still wins over both.

## State and carry

Stateless layouts return nil from `newState`. Otherwise `Workspace.state()`
builds the state lazily in `Workspace.lstate`, and a layout switch drops it
(`resetLayoutState`). The hooks, called after Workspace changed `Columns`:

- `inserted(w, at, kind, carry) unconsumed`: `kind` is `insertDefault`,
  `insertNewLane` or `insertJoinDisplaced`; `carry` is what `removed` returned
  elsewhere. It returns the carry it did not consume (nil when it took it).
- `removed(at, col) carry`: whatever should travel with the column.
- `settle(w)`: before `scroll()` measures; refresh keyed data, drop stale spans.

`Column.key` is the workspace-local column identity; state keys per-column data
by it. `Column.carry` is opaque (`any`): only the state that produced it reads
it. What `inserted` consumes is cleared from the column; what it returns stays
on the stored column, and a stateless workspace keeps all of it. The next
`takeColumn` passes it on when its own `removed` returns nil, so a column hops
through a scroll or fixed workspace and gets its cascade neighbors back. A
layout switch drops it with the state. Cascade keeps band starts by position
and push history by key (`cascadeState`).

On a layout switch `Monitor.applyNamed` selects the new layout, clears
maximization when the layout asks (`resetsMaximize`), resets the state, then
adjusts the view axis and capacity. Keep this order.

## Tests

`layout_conformance_*_test.go` iterate `conformanceLayouts()`: `layoutTable`
plus `testLayouts`, which test files extend from `init` (production never
mutates the table). `conformanceWorkspace` builds the one fixture: capacities
1-4, empty/single/many columns, maximized and fullscreen variants; each
invariant lives in one file. `layout_golden_test.go` pins reviewed rects per
layout. `layout_extension_test.go` defines its test layouts in that one file and injects
them through `Workspace.lay`. `layout_dispatch_test.go` checks Workspace results
through the generated composite mock (`newMocklayout`): expectations are set on it
directly, driving production code (gestures, spawn, overview), and the narrow
mocks serve tests that need one interface (`newMocklayoutState` for carry).
Never handwrite doubles. After an interface change run `make mocks` and commit
the generated files.

Hot paths: resolve the layout and build the `layoutView` once per call, not per
column (`layout_bench_test.go` benchmarks `columnRects`, `previewTiles` and
`borderAt` at 12 columns).

Required: `CGO_ENABLED=0 go build ./...`, `make check`, `make race`,
`make mocks-check`, `staticcheck ./...`, `gofmt -l internal/core` (empty) and
`CGO_ENABLED=1 go test -race -count=1 -run Alloc ./internal/core/`.

For end-to-end testing follow [headless.md](headless.md): a throwaway config per
layout, several windows, focus/move on both axes, grow/shrink, maximize,
overview and swipe, with `WAYLAND_DISPLAY` isolated and `NEFERWL_STATE` and
`NEFERWL_SLOT` unset.

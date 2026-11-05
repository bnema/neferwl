package statewire

import (
	"encoding/json"
	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/domain"
	"reflect"
	"strings"
	"testing"
)

// Stash and cell fields reach the file with their script-facing names.
func TestWindowScriptFields(t *testing.T) {
	got := window(domain.WindowState{ID: 3, Floating: true, Fullscreen: true, StashIndex: 2, StashCount: 4, Column: 5, Row: 6, Hidden: true, IdleInhibit: true, ShortcutsInhibited: true, PointerConstraint: "locked", Sandbox: "org.example.App", ContentType: "video", Xwayland: true}, workspaceid.WithPrefix("0a1b2c3d"))
	if !got.Floating || !got.Fullscreen || got.StashIndex != 2 || got.StashCount != 4 || got.Column != 5 || got.Row != 6 || !got.Hidden || !got.IdleInhibit || !got.ShortcutsInhibited || got.PointerConstraint != "locked" || got.Sandbox != "org.example.App" || got.ContentType != "video" || !got.Xwayland {
		t.Fatalf("%+v", got)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"floating":true`, `"fullscreen":true`, `"stash_index":2`, `"stash_count":4`, `"column":5`, `"row":6`, `"hidden":true`, `"idle_inhibit":true`, `"shortcuts_inhibited":true`, `"pointer_constraint":"locked"`, `"sandbox":"org.example.App"`, `"content_type":"video"`, `"xwayland":true`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("%s missing %s", data, key)
		}
	}
}

// A configured workspace is named after its configured name, any other one
// after the launch prefix and its core ID: the string of ext_workspace_handle_v1.
func TestWorkspaceIDStrings(t *testing.T) {
	ids := workspaceid.WithPrefix("0a1b2c3d")
	st := FromDomain(domain.State{
		Outputs: []domain.OutputState{{Name: "DP-2", Workspace: "web", WorkspaceID: 7}, {Name: "DP-3", WorkspaceID: 8}},
		Windows: []domain.WindowState{{ID: 1, WorkspaceID: 7, WorkspaceName: "web"}, {ID: 2, WorkspaceID: 8}},
	}, ids)
	if st.Outputs[0].WorkspaceID != "name:web" || st.Outputs[1].WorkspaceID != "0a1b2c3d-8" {
		t.Fatalf("outputs %+v", st.Outputs)
	}
	if st.Windows[0].WorkspaceID != "name:web" || st.Windows[1].WorkspaceID != "0a1b2c3d-8" {
		t.Fatalf("windows %+v", st.Windows)
	}
	data, err := json.Marshal(st.Windows[1])
	if err != nil || !strings.Contains(string(data), `"workspace_id":"0a1b2c3d-8"`) {
		t.Fatalf("%s %v", data, err)
	}
}

// Workspaces reach the file with their script-facing names.
func TestWorkspaceGeometryFields(t *testing.T) {
	ids := workspaceid.WithPrefix("0a1b2c3d")
	st := FromDomain(domain.State{Workspaces: []domain.WorkspaceState{
		{ID: 7, Number: 2, Name: "web", Output: "DP-2", Visible: true, Overflow: "fixed", Columns: []domain.ColumnState{
			{Rect: domain.Rect{X: 10, Y: 20, W: 300, H: 400}, Windows: []domain.TileState{{ID: 5, Rect: domain.Rect{X: 11, Y: 21, W: 298, H: 198}}}},
		}},
		{ID: 8, Output: "DP-2"},
	}}, ids)
	want := []Workspace{
		{ID: "name:web", Number: 2, Name: "web", Output: "DP-2", Visible: true, Overflow: "fixed", Columns: []Column{
			{X: 10, Y: 20, Width: 300, Height: 400, Windows: []Tile{{ID: 5, X: 11, Y: 21, Width: 298, Height: 198}}},
		}},
		{ID: "0a1b2c3d-8", Output: "DP-2", Columns: []Column{}},
	}
	if !reflect.DeepEqual(st.Workspaces, want) {
		t.Fatalf("%+v", st.Workspaces)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"workspaces":[`, `"id":"name:web"`, `"number":2`, `"visible":true`, `"overflow":"fixed"`, `"columns":[{"x":10,"y":20,"width":300,"height":400,"windows":[{"id":5,"x":11,"y":21,"width":298,"height":198}]}]`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("%s missing %s", data, key)
		}
	}
}

func TestHDRExpansionJSON(t *testing.T) {
	st := domain.State{Outputs: []domain.OutputState{{Name: "A", HDR: domain.OutputHDRState{Tier: "full", PeakNits: 604, PeakSource: "edid", Headroom: 604.0 / 203, Active: true, SDRExpand: "auto", Expandable: true}}}, Windows: []domain.WindowState{{ID: 1, SDRExpansion: domain.SDRExpansionState{Active: true, Reason: "eligible", Intensity: 0.5, Top: 1.9877, Decode: "gamma22"}}, {ID: 2, SDRExpansion: domain.SDRExpansionState{Reason: "not game"}}}}
	data, err := json.Marshal(FromDomain(st, workspaceid.WithPrefix("test")))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Outputs []struct {
			HDR map[string]any `json:"hdr"`
		}
		Windows []struct {
			Expansion map[string]any `json:"sdr_expansion"`
		}
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Outputs[0].HDR["peak_nits"] != float64(604) || got.Outputs[0].HDR["peak_source"] != "edid" || got.Outputs[0].HDR["sdr_expand"] != "auto" {
		t.Fatal(string(data))
	}
	if got.Windows[0].Expansion["intensity"] != 0.5 || got.Windows[0].Expansion["decode"] != "gamma22" {
		t.Fatal(string(data))
	}
	if len(got.Windows[1].Expansion) != 2 || got.Windows[1].Expansion["reason"] != "not game" {
		t.Fatal(string(data))
	}
}

func TestWorkspaceFocusedJSON(t *testing.T) {
	st := FromDomain(domain.State{Workspaces: []domain.WorkspaceState{{ID: 1, Focused: 42}, {ID: 2}}}, workspaceid.WithPrefix("test"))
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"focused":42`, `"focused":0`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
	var decoded State
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Workspaces[0].Focused != 42 || decoded.Workspaces[1].Focused != 0 {
		t.Fatalf("roundtrip: %+v", decoded.Workspaces)
	}
}

func TestWorkspaceCoverJSON(t *testing.T) {
	st := FromDomain(domain.State{
		Workspaces: []domain.WorkspaceState{{ID: 1, Cover: 7}, {ID: 2}},
		Windows:    []domain.WindowState{{ID: 7, Fullscreen: true}, {ID: 8, Fullscreen: true}},
	}, workspaceid.WithPrefix("test"))
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"cover":7`, `"cover":0`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
	var decoded State
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Workspaces[0].Cover != 7 || decoded.Workspaces[1].Cover != 0 {
		t.Fatalf("roundtrip: %+v", decoded.Workspaces)
	}
	if !decoded.Windows[0].Fullscreen || !decoded.Windows[1].Fullscreen {
		t.Fatalf("held member not fullscreen: %+v", decoded.Windows)
	}
}

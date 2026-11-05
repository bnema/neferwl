package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bnema/neferwl/internal/domain"
)

// ErrRuntimeOutputForbidden rejects removal of a config or fixed output.
var ErrRuntimeOutputForbidden = errors.New("only runtime-added outputs can be removed")

// RuntimeAdd starts the same backend transaction used for output configuration.
// The caller obtains completion through ReplyOut using id. An empty name selects
// the next free HEADLESS-n. Validation failures leave the desired set unchanged.
func (a *OutputApply) RuntimeAdd(id uint64, name string, mode domain.OutputMode) (string, error) {
	if a.running || a.pending {
		return "", errors.New("output apply already pending")
	}
	if !a.state.Headless() {
		return "", ErrRuntimeOutputForbidden
	}
	if mode.Width < 1 || mode.Width > domain.MaxOutputDim || mode.Height < 1 || mode.Height > domain.MaxOutputDim || mode.RefreshMilli < 0 {
		return "", errors.New("invalid virtual output mode")
	}
	used := func(name string) bool {
		for _, o := range a.state.Effective().Outputs {
			if o.Name == name {
				return true
			}
		}
		for _, h := range a.state.heads.Heads {
			if h.Info.Name == name {
				return true
			}
		}
		return false
	}
	if name == "" {
		for n := 1; ; n++ {
			candidate := fmt.Sprintf("HEADLESS-%d", n)
			if !used(candidate) {
				name = candidate
				break
			}
		}
	}
	if strings.ContainsAny(name, "/\\\x00 \t\n") || name == "." || name == ".." || used(name) {
		return "", errors.New("invalid or occupied output name")
	}
	if mode.RefreshMilli == 0 {
		mode.RefreshMilli = domain.DefaultRefreshMilli
	}
	a.beginRuntime(id)
	a.state.runtime = append(a.state.runtime, domain.OutputConfig{Name: name, Virtual: true, Mode: domain.FormatOutputMode(mode)})
	a.setConfig(a.state.Effective())
	a.due(a.state.Effective())
	return name, nil
}

// RuntimeRemove removes only runtime-added outputs, never file declarations or
// fixed heads. Reload keeps the runtime desired set independent of file edits.
func (a *OutputApply) RuntimeRemove(id uint64, name string) error {
	if a.running || a.pending {
		return errors.New("output apply already pending")
	}
	index := slices.IndexFunc(a.state.runtime, func(o domain.OutputConfig) bool { return o.Name == name })
	if !a.state.Headless() || index < 0 {
		return ErrRuntimeOutputForbidden
	}
	for _, o := range a.state.file.Outputs {
		if o.Name == name {
			return ErrRuntimeOutputForbidden
		}
	}
	a.beginRuntime(id)
	a.state.runtime = slices.Delete(slices.Clone(a.state.runtime), index, index+1)
	delete(a.state.overrides, name)
	a.setConfig(a.state.Effective())
	a.due(a.state.Effective())
	return nil
}

func (a *OutputApply) beginRuntime(id uint64) {
	a.previous = a.state.Snapshot()
	a.runtimePrevious = slices.Clone(a.state.runtime)
	a.runtimeChange = true
	a.req = &domain.OutputApply{ID: id}
	a.rollback, a.failure = false, nil
}

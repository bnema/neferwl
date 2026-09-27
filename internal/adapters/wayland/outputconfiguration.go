package wayland

import (
	"image"
	"math"

	"github.com/bnema/neferwl/internal/ports"
	wlr "github.com/bnema/purego-libwayland/protocol/wlroutputmanagement"
	"github.com/bnema/purego-libwayland/server"
)

type outputConfiguration struct {
	manager         *outputManager
	res             *wlr.ZwlrOutputConfigurationV1
	heads           map[string]*headConfiguration
	used, cancelled bool
	serial          uint32
}
type headConfiguration struct {
	parent                                                *outputConfiguration
	res                                                   *wlr.ZwlrOutputConfigurationHeadV1
	change                                                ports.HeadChange
	modeSet, positionSet, transformSet, scaleSet, syncSet bool
}

func (c *outputConfiguration) head(r *wlr.ZwlrOutputHeadV1) (string, bool) {
	if r == nil || !r.Resource.Alive() || r.Client() != c.res.Client() {
		return "", false
	}
	for name, h := range c.manager.heads {
		if h.res.Resource == r.Resource {
			return name, true
		}
	}
	return "", false
}
func (c *outputConfiguration) add(r *wlr.ZwlrOutputConfigurationV1, head *wlr.ZwlrOutputHeadV1, id uint32, enabled bool) {
	if c.used {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationV1ErrorAlreadyUsed), "configuration already used")
		return
	}
	name, ok := c.head(head)
	if !ok {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationV1ErrorAlreadyConfiguredHead), "unknown head")
		return
	}
	if _, exists := c.heads[name]; exists {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationV1ErrorAlreadyConfiguredHead), "head configured twice")
		return
	}
	h := &headConfiguration{parent: c, change: ports.HeadChange{Name: name, Enabled: enabled}}
	c.heads[name] = h
	if enabled {
		res, err := wlr.NewZwlrOutputConfigurationHeadV1(r.Client(), r.Version(), id, h)
		if err != nil {
			delete(c.heads, name)
			return
		}
		h.res = res
		res.OnDestroy = func() { h.res = nil }
	}
}
func (c *outputConfiguration) EnableHead(r *wlr.ZwlrOutputConfigurationV1, id uint32, head *wlr.ZwlrOutputHeadV1) {
	c.add(r, head, id, true)
}
func (c *outputConfiguration) DisableHead(r *wlr.ZwlrOutputConfigurationV1, head *wlr.ZwlrOutputHeadV1) {
	c.add(r, head, 0, false)
}
func (c *outputConfiguration) Destroy(*wlr.ZwlrOutputConfigurationV1) { c.used = true }
func (c *outputConfiguration) Test(r *wlr.ZwlrOutputConfigurationV1)  { c.submit(r, true) }
func (c *outputConfiguration) Apply(r *wlr.ZwlrOutputConfigurationV1) { c.submit(r, false) }
func (c *outputConfiguration) submit(r *wlr.ZwlrOutputConfigurationV1, test bool) {
	if c.used {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationV1ErrorAlreadyUsed), "configuration already used")
		return
	}
	c.used = true
	if c.manager.s.managementSerial != c.serial {
		c.cancelled = true
		r.SendCancelled()
		return
	}
	if len(c.heads) != len(c.manager.heads) {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationV1ErrorUnconfiguredHead), "not all heads configured")
		return
	}
	request := ports.OutputApply{ID: c.manager.s.nextOutputApply, Test: test}
	c.manager.s.nextOutputApply++
	for _, head := range c.manager.s.outputHeads.Heads {
		change, ok := c.heads[head.Info.Name]
		if !ok {
			r.PostError(uint32(wlr.ZwlrOutputConfigurationV1ErrorUnconfiguredHead), "not all heads configured")
			return
		}
		request.Heads = append(request.Heads, change.change)
	}
	if c.manager.s.channels.OutputApply == nil {
		r.SendFailed()
		return
	}
	c.manager.s.outputReplies[request.ID] = c
	select {
	case c.manager.s.channels.OutputApply <- request:
	default:
		delete(c.manager.s.outputReplies, request.ID)
		r.SendFailed()
	}
}
func (h *headConfiguration) once(field *bool, r *wlr.ZwlrOutputConfigurationHeadV1) bool {
	if *field {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationHeadV1ErrorAlreadySet), "property already set")
		return false
	}
	*field = true
	return true
}
func (h *headConfiguration) SetMode(r *wlr.ZwlrOutputConfigurationHeadV1, mode *wlr.ZwlrOutputModeV1) {
	if !h.once(&h.modeSet, r) {
		return
	}
	head := h.parent.manager.heads[h.change.Name]
	if head == nil || mode == nil || !mode.Resource.Alive() {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationHeadV1ErrorInvalidMode), "invalid mode")
		return
	}
	m, ok := head.modes[mode.Resource]
	if !ok {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationHeadV1ErrorInvalidMode), "invalid mode")
		return
	}
	h.change.Mode = &m
}
func (h *headConfiguration) SetCustomMode(r *wlr.ZwlrOutputConfigurationHeadV1, w, height, refresh int32) {
	if !h.once(&h.modeSet, r) {
		return
	}
	if w <= 0 || height <= 0 || refresh < 0 {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationHeadV1ErrorInvalidCustomMode), "invalid custom mode")
		return
	}
	h.change.CustomMode = true
	h.change.Mode = &ports.OutputMode{Width: int(w), Height: int(height), RefreshMilli: int(refresh)}
}
func (h *headConfiguration) SetPosition(r *wlr.ZwlrOutputConfigurationHeadV1, x, y int32) {
	if h.once(&h.positionSet, r) {
		h.change.Pos = &image.Point{X: int(x), Y: int(y)}
	}
}
func (h *headConfiguration) SetTransform(r *wlr.ZwlrOutputConfigurationHeadV1, v int32) {
	if !h.once(&h.transformSet, r) {
		return
	}
	if v < 0 || v > 7 {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationHeadV1ErrorInvalidTransform), "invalid transform")
		return
	}
	h.change.Transform = int(v)
}
func (h *headConfiguration) SetScale(r *wlr.ZwlrOutputConfigurationHeadV1, v server.Fixed) {
	if !h.once(&h.scaleSet, r) {
		return
	}
	scale := v.Float()
	if scale <= 0 || math.IsNaN(scale) {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationHeadV1ErrorInvalidScale), "invalid scale")
		return
	}
	h.change.Scale = scale
}
func (h *headConfiguration) SetAdaptiveSync(r *wlr.ZwlrOutputConfigurationHeadV1, v uint32) {
	if !h.once(&h.syncSet, r) {
		return
	}
	if v > 1 {
		r.PostError(uint32(wlr.ZwlrOutputConfigurationHeadV1ErrorInvalidAdaptiveSyncState), "invalid adaptive sync")
		return
	}
	enabled := v == 1
	h.change.AdaptiveSync = &enabled
}

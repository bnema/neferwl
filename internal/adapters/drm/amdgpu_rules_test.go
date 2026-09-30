package drm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// Connector HDR property IDs and values of the amdgpu rule tests.
const (
	pHDRMeta, pColorspace, pMaxBPC = 20, 21, 22
	colorspaceBT2020               = 9
)

// Blob lifetime is part of the model: attach feeds createBlob/destroyBlob
// mocks into the live set.
type amdgpuModel struct {
	t    *testing.T
	mu   sync.Mutex
	live map[uint32]bool
	next uint32
	rule func(*atomicReq, uint32) error
}

// newAmdgpuModel starts from a lit desktop whose mode blob 99 is live.
func newAmdgpuModel(t *testing.T) *amdgpuModel {
	t.Helper()
	m := &amdgpuModel{t: t, live: map[uint32]bool{99: true}, next: 100}
	m.rule = amdgpuRuleWith(t, m.isLive)
	return m
}

func (m *amdgpuModel) isLive(blob uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.live[blob]
}

// attach makes k create ids from 100 up and destroy only live blobs. With
// strict ids, each is expected to be destroyed exactly once and no other
// destroy is allowed; without, any live blob may be destroyed.
func (m *amdgpuModel) attach(k *mockkms, strict ...uint32) {
	k.EXPECT().createBlob(mock.Anything).RunAndReturn(func([]byte) (uint32, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		id := m.next
		m.next++
		m.live[id] = true
		return id, nil
	}).Maybe()
	destroy := func(id uint32) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.live[id] {
			m.t.Errorf("amdgpu: destroyBlob(%d) of an unknown or destroyed blob", id)
			return unix.ENOENT
		}
		delete(m.live, id)
		return nil
	}
	if len(strict) == 0 {
		k.EXPECT().destroyBlob(mock.Anything).RunAndReturn(destroy).Maybe()
	}
	for _, id := range strict {
		k.EXPECT().destroyBlob(id).RunAndReturn(destroy).Once()
	}
}

// amdgpuRule returns a commit rule encoding the atomic_check constraints of
// amdgpu_dm that real hardware applied to the protected requests (EINVAL on
// kernel 7.2, RX/DP+HDMI). The rule tracks the committed state, starting from
// a lit desktop: CRTC enabled and active, primary/cursor/overlay/stray
// planes attached, connector routed.
//
//   - a CRTC with MODE_ID set (enabled) needs its primary plane attached,
//     even with ACTIVE=0 (drm_atomic_helper_check_crtc_primary_plane);
//   - MODE_ID, connector CRTC_ID and ACTIVE agree: an active or routed CRTC
//     is enabled, an enabled one is routed;
//   - a plane's FB_ID and CRTC_ID are both set or both clear, and only on an
//     enabled CRTC;
//   - the active primary plane covers the whole mode (no partial src/dst);
//   - HDR_OUTPUT_METADATA needs the BT.2020 Colorspace;
//   - MODE_ID must name a live blob, and a change of MODE_ID, ACTIVE or the
//     connector CRTC_ID is a modeset that needs the ALLOW_MODESET flag.
//
// amdgpuRule has no blob model: any MODE_ID is live.
func amdgpuRule(t *testing.T) func(*atomicReq, uint32) error {
	t.Helper()
	return amdgpuRuleWith(t, func(uint32) bool { return true })
}

func amdgpuRuleWith(t *testing.T, live func(uint32) bool) func(*atomicReq, uint32) error {
	t.Helper()
	state := map[[2]uint32]uint64{
		{tCrtc, pMode}: 99, {tCrtc, pActive}: 1, {tConn, pConnCrtc}: tCrtc,
	}
	for _, p := range []uint32{tPrimary, tCursor, tOverlay, 53} {
		state[[2]uint32{p, pFB}], state[[2]uint32{p, pCrtcID}] = 70, tCrtc
	}
	return func(req *atomicReq, flags uint32) error {
		next := make(map[[2]uint32]uint64, len(state))
		for k, v := range state {
			next[k] = v
		}
		for i, obj := range req.objs {
			for _, p := range req.props[i] {
				next[[2]uint32{obj, p.prop}] = p.val
			}
		}
		get := func(obj, prop uint32) uint64 { return next[[2]uint32{obj, prop}] }
		if mode := get(tCrtc, pMode); mode != 0 && !live(uint32(mode)) {
			t.Logf("amdgpu: MODE_ID %d names an unknown or destroyed blob", mode)
			return unix.EINVAL
		}
		for _, k := range [...][2]uint32{{tCrtc, pMode}, {tCrtc, pActive}, {tConn, pConnCrtc}} {
			if next[k] != state[k] && flags&atomicAllowModes == 0 {
				t.Logf("amdgpu: modeset change of %v without ALLOW_MODESET", k)
				return unix.EINVAL
			}
		}
		enabled, active := get(tCrtc, pMode) != 0, get(tCrtc, pActive) == 1
		if active && !enabled || (get(tConn, pConnCrtc) != 0) != enabled {
			t.Logf("amdgpu: ACTIVE/MODE_ID/connector routing disagree: %v", next)
			return unix.EINVAL
		}
		for _, p := range []uint32{tPrimary, tCursor, tOverlay, 53} {
			fb, crtc := get(p, pFB), get(p, pCrtcID)
			if (fb != 0) != (crtc != 0) || crtc != 0 && (!enabled || crtc != tCrtc) {
				t.Logf("amdgpu: plane %d fb=%d crtc=%d enabled=%v", p, fb, crtc, enabled)
				return unix.EINVAL
			}
		}
		if enabled && get(tPrimary, pFB) == 0 {
			t.Log("amdgpu: enabled CRTC without primary plane")
			return unix.EINVAL
		}
		if active {
			for _, c := range [...]struct {
				prop uint32
				want uint64
			}{{10, 0}, {11, 0}, {12, 200 << 16}, {13, 100 << 16}, {14, 0}, {15, 0}, {16, 200}, {17, 100}} {
				if get(tPrimary, c.prop) != c.want {
					t.Logf("amdgpu: primary prop %d = %d, want full-screen %d", c.prop, get(tPrimary, c.prop), c.want)
					return unix.EINVAL
				}
			}
		}
		if get(tConn, pHDRMeta) != 0 && get(tConn, pColorspace) != colorspaceBT2020 {
			t.Log("amdgpu: HDR metadata without BT.2020 colorspace")
			return unix.EINVAL
		}
		if flags&atomicTestOnly == 0 {
			state = next
		}
		return nil
	}
}

func enableTestHDR(o *Output) {
	o.hdrOn, o.hdrBlob = true, 55
	o.hdrProps = connectorHDRProps{Metadata: pHDRMeta, Colorspace: pColorspace, MaxBPC: pMaxBPC, BT2020Value: colorspaceBT2020, MaxBPCValue: 8, HasDefault: true}
}

func TestAmdgpuRuleRejectsWhatHardwareRejected(t *testing.T) {
	for name, build := range map[string]func(*atomicReq){
		// The shape that failed on hardware: inactive, still enabled, no planes.
		"enabled CRTC without primary": func(r *atomicReq) {
			r.set(tCrtc, pActive, 0)
			for _, p := range []uint32{tPrimary, tCursor, tOverlay, 53} {
				r.set(p, pFB, 0)
				r.set(p, pCrtcID, 0)
			}
		},
		"plane on disabled CRTC": func(r *atomicReq) {
			r.set(tCrtc, pActive, 0)
			r.set(tCrtc, pMode, 0)
			r.set(tConn, pConnCrtc, 0)
		},
		"connector routed to disabled CRTC": func(r *atomicReq) {
			r.set(tCrtc, pActive, 0)
			r.set(tCrtc, pMode, 0)
			for _, p := range []uint32{tPrimary, tCursor, tOverlay, 53} {
				r.set(p, pFB, 0)
				r.set(p, pCrtcID, 0)
			}
		},
		"cursor with CRTC but no FB": func(r *atomicReq) { r.set(tCursor, pFB, 0) },
		"partial primary":            func(r *atomicReq) { r.set(tPrimary, 16, 100) },
		"unknown MODE_ID":            func(r *atomicReq) { r.set(tCrtc, pMode, 123) },
		"HDR metadata without colorspace": func(r *atomicReq) {
			r.set(tConn, pHDRMeta, 55)
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := &atomicReq{}
			build(req)
			if err := amdgpuRule(t)(req, atomicAllowModes); err == nil {
				t.Fatal("rule accepted a request amdgpu refuses")
			}
		})
	}
}

// Every protected disable/activation request must pass the amdgpu rules, with
// HDR on and off, and leave the state dark then black-only.
func TestProtectedRequestsAcceptedByAmdgpuRules(t *testing.T) {
	for _, hdr := range []bool{false, true} {
		name := map[bool]string{false: "sdr", true: "hdr"}[hdr]
		t.Run(name, func(t *testing.T) {
			m := newAmdgpuModel(t)
			o, k, commits, _ := testOutputRules(t, m.rule)
			m.attach(k, 99)
			if hdr {
				enableTestHDR(o)
			}
			o.modeBlob = 99
			o.overlay = &plane{id: tOverlay, props: planeProps}
			o.stray = []*plane{o.overlay, {id: 53, props: planeProps}}
			k.EXPECT().objProps(uint32(53), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, tCrtc}}, nil)
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().UseTarget(0).Return().Once()
			r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
			if err := o.protectedModeset(context.Background(), r); err != nil {
				t.Fatalf("amdgpu refused the protected transaction: %v", err)
			}
			if len(*commits) != 2 {
				t.Fatalf("commits %d", len(*commits))
			}
			act := (*commits)[1]
			if fb, _ := act.req.value(tPrimary, pFB); fb != 71 && fb != 70 {
				t.Fatalf("activated fb %d", fb)
			}
			if crtc, ok := act.req.value(tCursor, pCrtcID); !ok || crtc != 0 {
				t.Fatalf("cursor not explicitly detached: %d %v", crtc, ok)
			}
			if fb, ok := act.req.value(tCursor, pFB); !ok || fb != 0 {
				t.Fatalf("cursor fb not explicitly detached: %d %v", fb, ok)
			}
			if o.off || !o.protected {
				t.Fatalf("off=%v protected=%v", o.off, o.protected)
			}
			// The desktop mode blob is destroyed once by the disable; the
			// activation's blob is the only live one and o.modeBlob owns it.
			if o.modeBlob != 100 || !m.isLive(100) || m.isLive(99) {
				t.Fatalf("modeBlob %d live100=%v live99=%v", o.modeBlob, m.isLive(100), m.isLive(99))
			}
		})
	}
}

func TestAmdgpuRuleRejectsModesetWithoutAllowModes(t *testing.T) {
	for name, build := range map[string]func(*atomicReq){
		"MODE_ID":           func(r *atomicReq) { r.set(tCrtc, pMode, 99) },
		"ACTIVE":            func(r *atomicReq) { r.set(tCrtc, pActive, 0) },
		"connector CRTC_ID": func(r *atomicReq) { r.set(tConn, pConnCrtc, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			m := newAmdgpuModel(t)
			m.live[98] = true
			req := &atomicReq{}
			build(req)
			if name == "MODE_ID" {
				req.set(tCrtc, pMode, 98)
			}
			if err := m.rule(req, 0); err == nil {
				t.Fatal("rule accepted a modeset change without ALLOW_MODESET")
			}
		})
	}
}

func TestAmdgpuRuleRejectsDestroyedModeBlob(t *testing.T) {
	m := newAmdgpuModel(t)
	req := &atomicReq{}
	req.set(tCrtc, pMode, 99)
	for _, c := range [...]struct{ prop, val uint32 }{{12, 200 << 16}, {13, 100 << 16}, {16, 200}, {17, 100}} {
		req.set(tPrimary, c.prop, uint64(c.val))
	}
	if err := m.rule(req, atomicAllowModes); err != nil {
		t.Fatalf("live blob rejected: %v", err)
	}
	m.mu.Lock()
	delete(m.live, 99)
	m.mu.Unlock()
	if err := m.rule(req, atomicAllowModes); err == nil {
		t.Fatal("rule accepted a destroyed MODE_ID blob")
	}
}

func TestProtectedResumeAcceptedByAmdgpuRules(t *testing.T) {
	m := newAmdgpuModel(t)
	o, k, commits, _ := testOutputRules(t, m.rule)
	m.attach(k)
	o.modeBlob = 99
	o.protected = true
	o.overlay = &plane{id: tOverlay, props: planeProps}
	o.stray = []*plane{o.overlay, {id: 53, props: planeProps}}
	k.EXPECT().objProps(uint32(53), uint32(objPlane)).Return(map[string][2]uint64{"CRTC_ID": {pCrtcID, tCrtc}}, nil)
	if err := o.modeset(); err != nil { // resume while protected: dark, not stale
		t.Fatalf("amdgpu refused protected inactive modeset: %v", err)
	}
	if !o.off || len(*commits) != 1 {
		t.Fatalf("off=%v commits=%d", o.off, len(*commits))
	}
	if v, ok := (*commits)[0].req.value(tCrtc, pMode); !ok || v != 0 {
		t.Fatal("protected disable left the CRTC enabled")
	}
}

// The legacy request is refused by the rule; the output stays registered and
// protected, retries with bounded backoff and never reports proof.
func TestProtectedRefusalKeepsOutputProtectedAndBacksOff(t *testing.T) {
	o, k, commits, _ := testOutputRules(t, func(r *atomicReq, flags uint32) error {
		if v, ok := r.value(tCrtc, pActive); ok && v == 1 {
			return unix.EINVAL
		}
		return nil
	})
	k.EXPECT().createBlob(mock.Anything).Return(100, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	err := o.protectedModeset(context.Background(), r)
	if err == nil || !retryableProtected(err) || !errorsIsEINVAL(err) {
		t.Fatalf("refusal result %v", err)
	}
	if !o.protected || !o.off || o.back != 0 || len(*commits) != 2 {
		t.Fatalf("protected=%v off=%v back=%d commits=%d", o.protected, o.off, o.back, len(*commits))
	}
	var enabled = true
	if !o.commitFailed(err, &enabled) || !enabled || !o.protectBackoffActive() {
		t.Fatal("refusal not retryable with backoff")
	}
	d := time.Duration(0)
	for range 10 {
		d = nextProtectBackoff(d)
	}
	if d != protectRetryMax || nextProtectBackoff(0) != protectRetryMin {
		t.Fatalf("backoff not bounded: %v", d)
	}
}

func errorsIsEINVAL(err error) bool { return err != nil && errors.Is(err, unix.EINVAL) }

// Run: activation refused twice with EINVAL. The output keeps running, stays
// dark, and only the later accepted black activation produces proof.
func TestRunProtectedActivationRetriesWithoutStoppingOrEarlyProof(t *testing.T) {
	var activations, refusals atomic.Int32
	o, k, _, _ := testOutputRules(t, func(r *atomicReq, flags uint32) error {
		if v, ok := r.value(tCrtc, pActive); ok && v == 1 && flags&atomicTestOnly == 0 {
			activations.Add(1)
			if refusals.Add(1) <= 2 {
				return unix.EINVAL
			}
		}
		return nil
	})
	o.cursor = nil
	var state atomic.Uint64
	state.Store(3)
	securityGate(t, o, &state)
	events := make(chan ports.SecurityBackendEvent, 32)
	o.SecurityEvents = events
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w := protectionFence(t, false)
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, nil, nil, nil, nil, make(chan ports.OutputPresented, 8), nil, nil)
	}()
	for {
		select {
		case ev := <-events:
			if _, ok := ev.(ports.SecurityOutputProof); !ok {
				continue
			}
			if n := activations.Load(); n < 3 {
				t.Fatalf("proof after %d activation attempts; refused ones are no evidence", n)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("output stopped: %v", err)
			}
			return
		case err := <-done:
			t.Fatalf("output stopped on refused protected commit: %v", err)
		case <-ctx.Done():
			t.Fatal("no proof after retries")
		}
	}
}

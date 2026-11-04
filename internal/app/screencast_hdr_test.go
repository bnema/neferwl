//go:build screencast

package app

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
)

// pwVideoFormat is the negotiated Format param of a node, as pw-dump shows it.
type pwVideoFormat struct {
	Format           string `json:"format"`
	TransferFunction string `json:"transferFunction"`
	ColorPrimaries   string `json:"colorPrimaries"`
	// Modifier is set only when DMA-BUF buffers were negotiated.
	Modifier any `json:"modifier"`
}

// nodeFormat reads node's negotiated Format with pw-dump; ok is false while
// none is negotiated.
func nodeFormat(t *testing.T, node uint32) (pwVideoFormat, bool) {
	t.Helper()
	out, err := exec.Command("pw-dump", itoa(node)).Output()
	if err != nil {
		t.Fatalf("pw-dump: %v", err)
	}
	// Only the negotiated Format is decoded: EnumFormat holds choices.
	var objs []struct {
		Info struct {
			Params struct {
				Format []json.RawMessage `json:"Format"`
			} `json:"params"`
		} `json:"info"`
	}
	if err := json.Unmarshal(out, &objs); err != nil {
		t.Fatalf("pw-dump output: %v", err)
	}
	for _, o := range objs {
		for _, raw := range o.Info.Params.Format {
			var f pwVideoFormat
			if json.Unmarshal(raw, &f) == nil && f.Format != "" {
				return f, true
			}
		}
	}
	return pwVideoFormat{}, false
}

// A cast of a virtual HDR output negotiates 10-bit PQ BT.2020 DMA-BUF
// buffers with a consumer that accepts it, and frames flow in that format.
//
//testpolicy:realtime real PipeWire/GStreamer negotiation and D-Bus lifecycle
func TestScreencastHDRVirtualOutput(t *testing.T) {
	requireCastEnv(t)
	cfg, warnings, err := config.Parse(strings.NewReader("output.tv = virtual 320x240@60\noutput.tv.hdr = on\n"))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("config: %v %v", err, warnings)
	}
	app := startCastAppConfig(t, "*\n", cfg)
	c := castClient(t)
	waitOwner(t, c, scName)
	var session, stream string
	timeout := time.After(15 * time.Second)
	for {
		name, m := call(c, scPath, "org.gnome.Mutter.ScreenCast", "CreateSession", "a{sv}", emptyProps)
		if name != "" {
			t.Fatalf("CreateSession: %s", name)
		}
		session = strings.Clone(m.Body().ObjectPath())
		var errName string
		if errName, stream = recordMonitor(c, session, "tv"); errName == "" {
			break
		}
		call(c, session, scSession, "Stop", "", nil)
		select {
		case <-timeout:
			t.Fatalf("RecordMonitor(tv): %s", errName)
		case <-app.inventory:
		}
	}
	if err := c.AddMatch("type='signal',interface='" + scStream + "'"); err != nil {
		t.Fatal(err)
	}
	if name, _ := call(c, session, scSession, "Start", "", nil); name != "" {
		t.Fatalf("Start: %s", name)
	}
	node := uint32(0)
	for node == 0 {
		m, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if m.Member == "PipeWireStreamAdded" && m.Path == stream {
			node = m.Body().Uint32()
		}
	}
	// The consumer accepts any raw format: it takes the producer's first.
	gstLog := &strings.Builder{}
	// The buffers are device-local DMA-BUFs a CPU sink cannot map: fakesink
	// takes them unmapped. Their PQ content is checked on the GPU
	// (vulkan.TestCastCopyPQ).
	gst := exec.Command("gst-launch-1.0", "-v", "pipewiresrc", "path="+itoa(node), "stream-properties=props,media.type=Video", "num-buffers=30", "!", "fakesink", "silent=false")
	gst.Stdout, gst.Stderr = gstLog, gstLog
	t.Cleanup(func() {
		if t.Failed() {
			dump, _ := exec.Command("pw-dump", itoa(node)).Output()
			t.Logf("gst:\n%s\npw-dump %d:\n%s", gstLog.String(), node, dump)
		}
	})
	if err := gst.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- gst.Wait() }()
	var got pwVideoFormat
	deadline := time.After(15 * time.Second)
	for ok := false; !ok; {
		select {
		case err := <-exited:
			t.Fatalf("consumer exited before negotiation: %v", err)
		case <-deadline:
			_ = gst.Process.Kill()
			t.Fatal("no negotiated format")
		case <-time.After(50 * time.Millisecond):
		}
		got, ok = nodeFormat(t, node)
	}
	t.Logf("node %d negotiated %+v", node, got)
	if got.Format != "xBGR_210LE" || got.TransferFunction != "smpte2084" || got.ColorPrimaries != "bt2020" || got.Modifier == nil {
		t.Errorf("negotiated %+v, want DMA-BUF xBGR_210LE smpte2084 bt2020", got)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("gst-launch-1.0 (30 PQ buffers): %v", err)
		}
	case <-time.After(30 * time.Second):
		_ = gst.Process.Kill()
		t.Fatal("gst-launch-1.0 got no 30 buffers")
	}
	if n := strings.Count(gstLog.String(), "chain "); n < 30 {
		t.Errorf("consumer got %d buffers, want 30", n)
	}
	call(c, session, scSession, "Stop", "", nil)
}

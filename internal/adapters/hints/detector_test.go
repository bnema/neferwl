package hints

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// u32, u64 and f32 build the byte fixtures of the protocol.
func u32(v uint32) []byte  { return binary.LittleEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte  { return binary.LittleEndian.AppendUint64(nil, v) }
func f32(v float32) []byte { return u32(math.Float32bits(v)) }
func cat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

func TestWriteRequest(t *testing.T) {
	var buf bytes.Buffer
	pix := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	require.NoError(t, writeRequest(&buf, 0x0102030405, detFrame{width: 2, height: 1, stride: 12, m: &mapping{b: append(pix, 99)}}))
	want := cat([]byte("NHRQ"), u32(1), u64(0x0102030405), u32(2), u32(1), u32(12), u32(1), u32(0), f32(0), pix)
	require.Equal(t, want, buf.Bytes())
}

func TestWriteRequestRegions(t *testing.T) {
	var buf bytes.Buffer
	pix := []byte{1, 2, 3, 4}
	f := detFrame{width: 1, height: 1, stride: 4, m: &mapping{b: pix}, regions: []region{{0, 1, 2, 3}, {4, 5, 6, 7}}}
	require.NoError(t, writeRequest(&buf, 1, f))
	want := cat([]byte("NHRQ"), u32(1), u64(1), u32(1), u32(1), u32(4), u32(1), u32(2),
		u32(0), u32(1), u32(2), u32(3), u32(4), u32(5), u32(6), u32(7), f32(0), pix)
	require.Equal(t, want, buf.Bytes())
}

func TestReadHello(t *testing.T) {
	dev, err := readHello(bytes.NewReader(cat([]byte("NHNT"), u32(1), u32(0), u32(6), []byte("Vulkan"), u32(1280))))
	require.NoError(t, err)
	require.Equal(t, "Vulkan", dev)

	_, err = readHello(bytes.NewReader(cat([]byte("NHNT"), u32(1), u32(1), u32(4), []byte("boom"))))
	require.ErrorIs(t, err, errDetector)
	require.ErrorContains(t, err, "boom")

	_, err = readHello(bytes.NewReader(cat([]byte("XXXX"), u32(1))))
	require.ErrorIs(t, err, errProtocol)
	_, err = readHello(bytes.NewReader(cat([]byte("NHNT"), u32(2), u32(0))))
	require.ErrorIs(t, err, errProtocol)
	_, err = readHello(bytes.NewReader(cat([]byte("NHNT"), u32(1), u32(0), u32(1<<20))))
	require.ErrorIs(t, err, errProtocol)
	_, err = readHello(bytes.NewReader([]byte("NHNT")))
	require.Error(t, err)
}

func TestReadResponse(t *testing.T) {
	head := func(seq uint64, status uint32) []byte { return cat([]byte("NHRS"), u32(1), u64(seq), u32(status)) }

	ok := cat(head(5, 0), f32(12.5), u32(2),
		f32(1), f32(2), f32(30), f32(40), f32(0.9), u32(0),
		f32(5), f32(6), f32(7), f32(8), f32(0.25), u32(3))
	got, err := readResponse(bytes.NewReader(ok), 5)
	require.NoError(t, err)
	require.Equal(t, []detection{{x1: 1, y1: 2, x2: 30, y2: 40, score: 0.9}, {x1: 5, y1: 6, x2: 7, y2: 8, score: 0.25, region: 3}}, got)

	got, err = readResponse(bytes.NewReader(cat(head(5, 0), f32(1), u32(0))), 5)
	require.NoError(t, err)
	require.Empty(t, got)

	_, err = readResponse(bytes.NewReader(cat(head(5, 1), u32(3), []byte("bad"))), 5)
	require.ErrorIs(t, err, errDetector)
	require.ErrorContains(t, err, "bad")

	_, err = readResponse(bytes.NewReader(ok), 6) // wrong sequence
	require.ErrorIs(t, err, errProtocol)
	_, err = readResponse(bytes.NewReader(cat([]byte("NHRX"), u32(1))), 5)
	require.ErrorIs(t, err, errProtocol)
	_, err = readResponse(bytes.NewReader(cat([]byte("NHRS"), u32(9), u64(5), u32(0))), 5)
	require.ErrorIs(t, err, errProtocol)
	_, err = readResponse(bytes.NewReader(cat(head(5, 0), f32(1), u32(1<<30))), 5)
	require.ErrorIs(t, err, errProtocol)
	_, err = readResponse(bytes.NewReader(ok[:len(ok)-3]), 5) // truncated
	require.Error(t, err)
}

func TestLineLog(t *testing.T) {
	var lines []string
	l := &lineLog{log: func(s string) { lines = append(lines, s) }}
	_, _ = l.Write([]byte("one\ntw"))
	_, _ = l.Write([]byte("o\n\nthree"))
	require.Equal(t, []string{"one", "two"}, lines)
}

// The real detector, when the environment names it:
// NEFERWL_TEST_DETECTOR (binary) and NEFERWL_TEST_DETECTOR_MODEL (models dir).
func TestProcDetectorIntegration(t *testing.T) {
	bin, model := os.Getenv("NEFERWL_TEST_DETECTOR"), os.Getenv("NEFERWL_TEST_DETECTOR_MODEL")
	if bin == "" || model == "" {
		t.Skip("NEFERWL_TEST_DETECTOR and NEFERWL_TEST_DETECTOR_MODEL not set")
	}
	d := newProcDetector(bin, model, 0.05)
	t.Cleanup(func() { _ = d.Close() })
	m := &mapping{b: make([]byte, 1280*720*4), live: new(int)}
	for i := range m.b {
		m.b[i] = 0xf0 // a blank light frame
	}
	f := detFrame{width: 1280, height: 720, stride: 1280 * 4, m: m}
	ctx := context.Background()

	dets, err := d.Detect(ctx, f)
	require.NoError(t, err)
	require.Empty(t, dets) // nothing clickable on a blank frame
	first := d.proc
	require.NotNil(t, first)
	_, err = d.Detect(ctx, f)
	require.NoError(t, err)
	require.Same(t, first, d.proc, "the child is kept alive between scans")

	// A dead child fails one scan, and the next one starts a new child.
	require.NoError(t, first.cmd.Process.Kill())
	time.Sleep(50 * time.Millisecond)
	_, err = d.Detect(ctx, f)
	require.Error(t, err)
	require.Nil(t, d.proc)
	_, err = d.Detect(ctx, f)
	require.NoError(t, err)
	require.NotSame(t, first, d.proc)

	// A wrong geometry is the detector's own error: the child stays.
	proc := d.proc
	_, err = d.Detect(ctx, detFrame{width: 1280, height: 720, stride: 4, m: m})
	require.ErrorIs(t, err, errDetector)
	require.Same(t, proc, d.proc)
	require.NoError(t, d.Close())
}

func TestProcDetectorMissingBinary(t *testing.T) {
	d := newProcDetector("/nonexistent/nefer-hints", t.TempDir(), 0.05)
	_, err := d.Detect(context.Background(), detFrame{width: 1, height: 1, stride: 4, m: &mapping{b: make([]byte, 4)}})
	require.Error(t, err)
	require.Nil(t, d.proc)
	require.NoError(t, d.Close())
}

func TestProcDetectorCommand(t *testing.T) {
	for _, threshold := range []float64{0, 0.3} {
		d := newProcDetector("nefer-hints", "/models", threshold)
		want := []string{"nefer-hints", "--model", "/models", "--imgsz", "1280", "--gpu", "auto"}
		if threshold != 0 {
			want = append(want, "--conf", "0.3")
		}
		require.Equal(t, want, d.cmd().Args)
	}
}

package hints

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
)

// detFrame is a captured output as the detector receives it: BGRA pixels in
// m, valid only during the Detect call. It holds the mapping, not the bytes,
// so that recording a call (mocks print their arguments) never reads memory
// that was unmapped since.
type detFrame struct {
	width, height, stride int
	m                     *mapping
}

// detection is one box found by the detector, in frame (physical) pixels.
type detection struct {
	x1, y1, x2, y2, score float32
}

// detector finds clickable elements in a frame. The worker goroutine is its
// only caller.
type detector interface {
	// Detect returns the boxes found in f. An error is for this call only:
	// the detector recovers by itself on the next one.
	Detect(ctx context.Context, f detFrame) ([]detection, error)
	// Close releases the detector. It is called once, by the worker
	// goroutine, when the worker returns.
	Close() error
}

const (
	protocolVersion = 1
	helloTimeout    = 10 * time.Second
	requestTimeout  = 2 * time.Second
	imgsz           = 1280
	// maxString and maxBoxes bound what a misbehaving child can make us
	// allocate.
	maxString = 1 << 16
	maxBoxes  = 1 << 20
)

var (
	magicHello    = [4]byte{'N', 'H', 'N', 'T'}
	magicRequest  = [4]byte{'N', 'H', 'R', 'Q'}
	magicResponse = [4]byte{'N', 'H', 'R', 'S'}

	// errProtocol is a framing error: the stream cannot be trusted any more.
	errProtocol = errors.New("hints: detector protocol error")
	// errDetector is an error the detector reported itself; it keeps serving.
	errDetector = errors.New("hints: detector error")
)

var le = binary.LittleEndian

// writeRequest writes one whole-frame request (no region, default
// confidence) followed by the pixels.
func writeRequest(w io.Writer, seq uint64, f detFrame) error {
	var h [4 + 4 + 8 + 5*4 + 4]byte
	copy(h[0:], magicRequest[:])
	le.PutUint32(h[4:], protocolVersion)
	le.PutUint64(h[8:], seq)
	le.PutUint32(h[16:], uint32(f.width))
	le.PutUint32(h[20:], uint32(f.height))
	le.PutUint32(h[24:], uint32(f.stride))
	le.PutUint32(h[28:], formatXRGB8888)
	le.PutUint32(h[32:], 0)                   // nregions
	le.PutUint32(h[36:], math.Float32bits(0)) // confidence: startup default
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(f.m.b[:f.height*f.stride])
	return err
}

func readMagic(r io.Reader, want [4]byte) error {
	var m [4]byte
	if _, err := io.ReadFull(r, m[:]); err != nil {
		return err
	}
	if m != want {
		return fmt.Errorf("%w: bad magic %q", errProtocol, m[:])
	}
	return nil
}

func readU32(r io.Reader) (uint32, error) {
	var b [4]byte
	_, err := io.ReadFull(r, b[:])
	return le.Uint32(b[:]), err
}

func readString(r io.Reader) (string, error) {
	n, err := readU32(r)
	if err != nil {
		return "", err
	}
	if n > maxString {
		return "", fmt.Errorf("%w: string of %d bytes", errProtocol, n)
	}
	b := make([]byte, n)
	_, err = io.ReadFull(r, b)
	return string(b), err
}

// readHello reads the hello: the device name, or the error of a failed start.
func readHello(r io.Reader) (device string, err error) {
	if err := readMagic(r, magicHello); err != nil {
		return "", err
	}
	version, err := readU32(r)
	if err != nil {
		return "", err
	}
	if version != protocolVersion {
		return "", fmt.Errorf("%w: version %d", errProtocol, version)
	}
	status, err := readU32(r)
	if err != nil {
		return "", err
	}
	msg, err := readString(r)
	if err != nil {
		return "", err
	}
	if status != 0 {
		return "", fmt.Errorf("%w: start failed: %s", errDetector, msg)
	}
	if _, err := readU32(r); err != nil { // imgsz
		return "", err
	}
	return msg, nil
}

// readResponse reads the response to request seq.
func readResponse(r io.Reader, seq uint64) ([]detection, error) {
	if err := readMagic(r, magicResponse); err != nil {
		return nil, err
	}
	var h [16]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	if v := le.Uint32(h[0:]); v != protocolVersion {
		return nil, fmt.Errorf("%w: version %d", errProtocol, v)
	}
	if got := le.Uint64(h[4:]); got != seq {
		return nil, fmt.Errorf("%w: response %d to request %d", errProtocol, got, seq)
	}
	if status := le.Uint32(h[12:]); status != 0 {
		msg, err := readString(r)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: status %d: %s", errDetector, status, msg)
	}
	var t [8]byte // inference ms, nboxes
	if _, err := io.ReadFull(r, t[:]); err != nil {
		return nil, err
	}
	n := le.Uint32(t[4:])
	if n > maxBoxes {
		return nil, fmt.Errorf("%w: %d boxes", errProtocol, n)
	}
	out := make([]detection, 0, min(n, 4096))
	var rec [24]byte
	for range n {
		if _, err := io.ReadFull(r, rec[:]); err != nil {
			return nil, err
		}
		f := func(i int) float32 { return math.Float32frombits(le.Uint32(rec[i*4:])) }
		out = append(out, detection{x1: f(0), y1: f(1), x2: f(2), y2: f(3), score: f(4)})
	}
	return out, nil
}

// procDetector runs the nefer-hints binary as a persistent child. It starts
// on the first Detect, and again after the child died or misbehaved.
type procDetector struct {
	command, model string
	conf           float64
	helloTimeout   time.Duration
	reqTimeout     time.Duration

	proc *process
	seq  uint64
}

func newProcDetector(command, model string, threshold float64) *procDetector {
	return &procDetector{command: command, model: model, conf: threshold, helloTimeout: helloTimeout, reqTimeout: requestTimeout}
}

// process is one running child.
type process struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	once sync.Once
}

// kill stops the child and reaps it; safe to call more than once.
func (p *process) kill() {
	p.once.Do(func() {
		_ = p.cmd.Process.Kill()
		_ = p.in.Close()
		_ = p.cmd.Wait()
	})
}

// interrupt kills the child without waiting: it unblocks a pending read or
// write from a timer or a context callback. kill reaps it afterwards.
func (p *process) interrupt() { _ = p.cmd.Process.Kill() }

func (d *procDetector) start(ctx context.Context) error {
	log := logging.For(ctx, "hints")
	cmd := exec.Command(d.command, "--model", d.model, "--imgsz", strconv.Itoa(imgsz),
		"--conf", strconv.FormatFloat(d.conf, 'f', -1, 64), "--gpu", "auto")
	cmd.Stderr = &lineLog{log: func(line string) { log.Debug().Str("detector", d.command).Msg(line) }}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("hints: start detector: %w", err)
	}
	p := &process{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<16)}
	t0 := time.Now()
	timer := time.AfterFunc(d.helloTimeout, p.interrupt)
	stop := context.AfterFunc(ctx, p.interrupt)
	device, err := readHello(p.out)
	stop()
	timedOut := !timer.Stop()
	if err != nil {
		p.kill()
		if timedOut {
			err = errors.New("hello timed out")
		}
		return fmt.Errorf("hints: detector hello: %w", err)
	}
	d.proc = p
	log.Info().Str("device", device).Float64("start_ms", ms(time.Since(t0))).Msg("hint detector ready")
	return nil
}

func (d *procDetector) Detect(ctx context.Context, f detFrame) ([]detection, error) {
	if d.proc == nil {
		if err := d.start(ctx); err != nil {
			return nil, err
		}
	}
	p := d.proc
	d.seq++
	timer := time.AfterFunc(d.reqTimeout, p.interrupt)
	stop := context.AfterFunc(ctx, p.interrupt)
	dets, err := d.roundTrip(p, f)
	stop()
	timedOut := !timer.Stop()
	if err == nil {
		return dets, nil
	}
	if errors.Is(err, errDetector) {
		return nil, err // the child answered and keeps serving
	}
	d.drop()
	switch {
	case timedOut:
		return nil, fmt.Errorf("hints: detector timed out after %s", d.reqTimeout)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("hints: detector failed: %w", err)
}

func (d *procDetector) roundTrip(p *process, f detFrame) ([]detection, error) {
	bw := bufio.NewWriterSize(p.in, 1<<16)
	if err := writeRequest(bw, d.seq, f); err != nil {
		return nil, err
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}
	return readResponse(p.out, d.seq)
}

// drop kills the current child: the next Detect starts a new one.
func (d *procDetector) drop() {
	if d.proc != nil {
		d.proc.kill()
		d.proc = nil
	}
}

// Close asks the child to stop by closing its stdin (it exits 0 on EOF), then
// kills it if it does not.
func (d *procDetector) Close() error {
	p := d.proc
	d.proc = nil
	if p == nil {
		return nil
	}
	_ = p.in.Close()
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
	return nil
}

// lineLog is an io.Writer that hands each complete line to log.
type lineLog struct {
	log func(string)
	buf []byte
}

func (l *lineLog) Write(b []byte) (int, error) {
	l.buf = append(l.buf, b...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if line := string(l.buf[:i]); line != "" {
			l.log(line)
		}
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > maxString {
		l.buf = l.buf[:0]
	}
	return len(b), nil
}

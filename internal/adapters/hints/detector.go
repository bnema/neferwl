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
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
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
	// regions are the crops detected independently; none means the whole
	// frame.
	regions []region
}

// detection is one box found by the detector, in frame (physical) pixels.
type detection struct {
	x1, y1, x2, y2, score float32
	// region is the index of the request region the box comes from.
	region int
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
	maxBoxes  = 64 * 1000 // runner default max-det per region, at most 64 regions
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

// writeRequest writes one request (the regions of f, default confidence)
// followed by the pixels.
func writeRequest(w io.Writer, seq uint64, f detFrame) error {
	var h [4 + 4 + 8 + 5*4]byte
	copy(h[0:], magicRequest[:])
	le.PutUint32(h[4:], protocolVersion)
	le.PutUint64(h[8:], seq)
	le.PutUint32(h[16:], uint32(f.width))
	le.PutUint32(h[20:], uint32(f.height))
	le.PutUint32(h[24:], uint32(f.stride))
	le.PutUint32(h[28:], formatXRGB8888)
	le.PutUint32(h[32:], uint32(len(f.regions)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	var rec [16]byte
	for _, r := range f.regions {
		le.PutUint32(rec[0:], uint32(int32(r.x)))
		le.PutUint32(rec[4:], uint32(int32(r.y)))
		le.PutUint32(rec[8:], uint32(r.w))
		le.PutUint32(rec[12:], uint32(r.h))
		if _, err := w.Write(rec[:]); err != nil {
			return err
		}
	}
	var conf [4]byte // confidence: startup default
	le.PutUint32(conf[:], math.Float32bits(0))
	if _, err := w.Write(conf[:]); err != nil {
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
		out = append(out, detection{x1: f(0), y1: f(1), x2: f(2), y2: f(3), score: f(4), region: int(le.Uint32(rec[20:]))})
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

// waitDelay bounds cmd.Wait once the child is dead: a descendant that kept
// stderr open must not block the reaping.
const (
	waitDelay  = time.Second
	closeGrace = time.Second
)

// process is one running child, alone in its process group. It keeps both
// pipe ends so that closing them unblocks a pending read or write.
type process struct {
	cmd *exec.Cmd
	in  *os.File // write end of the child's stdin
	out *os.File // read end of the child's stdout
	rd  *bufio.Reader

	mu     sync.Mutex
	reaped bool
	once   sync.Once
}

// interrupt kills the whole process group and closes our pipe ends, without
// waiting: it unblocks a pending read or write from a timer or a context
// callback. kill reaps the child afterwards.
func (p *process) interrupt() {
	p.mu.Lock()
	if !p.reaped {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
	p.mu.Unlock()
	_ = p.out.Close()
	_ = p.in.Close()
}

// kill stops the child and its descendants and reaps it; safe to call more
// than once. Wait is bounded by cmd.WaitDelay.
func (p *process) kill() {
	p.interrupt()
	p.wait()
}

// wait reaps the child (once); bounded by cmd.WaitDelay after its death.
func (p *process) wait() {
	p.once.Do(func() {
		_ = p.cmd.Wait()
		p.mu.Lock()
		p.reaped = true
		p.mu.Unlock()
	})
}

// cmd leaves confidence selection to the runner unless explicitly configured.
func (d *procDetector) cmd() *exec.Cmd {
	args := []string{"--model", d.model, "--imgsz", strconv.Itoa(imgsz), "--gpu", "auto"}
	if d.conf != 0 {
		args = append(args, "--conf", strconv.FormatFloat(d.conf, 'f', -1, 64))
	}
	return exec.Command(d.command, args...)
}

// interruptOnDeadline binds both callbacks to this process only. Stopping joins
// any callback that already started, so its result can never be accepted.
func interruptOnDeadline(ctx context.Context, timeout time.Duration, p *process) func() (timedOut, canceled bool) {
	timerDone, contextDone := make(chan struct{}), make(chan struct{})
	timer := time.AfterFunc(timeout, func() { defer close(timerDone); p.interrupt() })
	stop := context.AfterFunc(ctx, func() { defer close(contextDone); p.interrupt() })
	return func() (bool, bool) {
		timedOut, canceled := !timer.Stop(), !stop()
		if timedOut {
			<-timerDone
		}
		if canceled {
			<-contextDone
		}
		return timedOut, canceled
	}
}

func (d *procDetector) start(ctx context.Context) error {
	log := logging.For(ctx, "hints")
	cmd := d.cmd()
	cmd.Stderr = &lineLog{log: func(line string) { log.Debug().Str("detector", d.command).Msg(line) }}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	inR, inW, err := os.Pipe()
	if err != nil {
		return err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return err
	}
	cmd.Stdin, cmd.Stdout = inR, outW
	err = cmd.Start()
	// The child has its ends; ours are not needed any more.
	_ = inR.Close()
	_ = outW.Close()
	if err != nil {
		_ = inW.Close()
		_ = outR.Close()
		return fmt.Errorf("hints: start detector: %w", err)
	}
	p := &process{cmd: cmd, in: inW, out: outR, rd: bufio.NewReaderSize(outR, 1<<16)}
	t0 := time.Now()
	stop := interruptOnDeadline(ctx, d.helloTimeout, p)
	device, err := readHello(p.rd)
	timedOut, canceled := stop()
	if err != nil || timedOut || canceled {
		p.kill()
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if timedOut {
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
	stop := interruptOnDeadline(ctx, d.reqTimeout, p)
	dets, err := d.roundTrip(p, f)
	timedOut, canceled := stop()
	if timedOut || canceled {
		d.drop()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("hints: detector timed out after %s", d.reqTimeout)
	}
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
	return readResponse(p.rd, d.seq)
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
	defer func() { _ = p.out.Close() }()
	_ = p.in.Close()
	done := make(chan struct{})
	go func() { p.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(closeGrace):
		p.interrupt()
		select {
		case <-done:
		case <-time.After(2 * waitDelay):
		}
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

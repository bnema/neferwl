package hints

import (
	"context"
	"image"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The detector child of these tests is the test binary itself, re-executed:
// a real process, started with the detector's own arguments. TestMain turns
// it into helperChild when helperEnv is set.
const (
	helperEnv      = "NEFERWL_HINTS_HELPER"
	helperPIDFile  = "NEFERWL_HINTS_HELPER_PIDFILE"
	helperChildPID = "NEFERWL_HINTS_HELPER_CHILDPID"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helperChild(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helperHello() []byte {
	return cat(magicHello[:], u32(protocolVersion), u32(0), u32(4), []byte("test"), u32(imgsz))
}

// helperChild plays the detector for mode:
//
//	nohello      never says hello
//	silent       says hello, then never answers
//	grandchild   like nohello, after spawning a grandchild that shares the
//	             child's stdout, stderr and process group
//	ok           says hello and answers every request with no box
func helperChild(mode string) {
	if f := os.Getenv(helperChildPID); f != "" {
		_ = os.WriteFile(f, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	if mode == "grandchild" {
		gc := exec.Command("sleep", "60")
		gc.Stdout, gc.Stderr = os.Stdout, os.Stderr
		if err := gc.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv(helperPIDFile), []byte(strconv.Itoa(gc.Process.Pid)), 0o600)
		mode = "nohello"
	}
	if mode != "nohello" {
		_, _ = os.Stdout.Write(helperHello())
	}
	if mode != "ok" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	for {
		var h [40]byte
		if _, err := io.ReadFull(os.Stdin, h[:]); err != nil {
			return
		}
		_, err := io.CopyN(io.Discard, os.Stdin, int64(le.Uint32(h[20:])*le.Uint32(h[24:])))
		if err != nil {
			return
		}
		_, _ = os.Stdout.Write(cat(magicResponse[:], u32(protocolVersion), h[8:16], u32(0), u32(0), u32(0)))
	}
}

func helperDetector(t *testing.T, mode string) *procDetector {
	t.Helper()
	t.Setenv(helperEnv, mode)
	d := newProcDetector(os.Args[0], t.TempDir(), 0.05)
	d.helloTimeout, d.reqTimeout = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func tinyFrame() detFrame {
	return detFrame{crop: image.Rect(0, 0, 1, 1), stride: 4, m: &mapping{b: make([]byte, 4), live: new(int)}}
}

// processGone reports whether pid is dead (or a zombie waiting for its
// reaper).
func processGone(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	s := string(b)
	return strings.HasPrefix(s[strings.LastIndexByte(s, ')')+2:], "Z")
}

func readPID(t *testing.T, file string) int {
	t.Helper()
	var pid int
	waitRealIO(t, func() bool {
		b, err := os.ReadFile(file)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(b))
		return err == nil
	}, "child PID file")
	return pid
}

func TestProcDetectorHungChildIsKilledAndRestarted(t *testing.T) {
	for _, mode := range []string{"nohello", "silent"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := t.TempDir() + "/child"
			t.Setenv(helperChildPID, pidFile)
			d := helperDetector(t, mode)
			ctx := context.Background()
			start := time.Now()
			_, err := d.Detect(ctx, tinyFrame())
			require.ErrorContains(t, err, "timed out")
			if mode == "nohello" {
				require.ErrorContains(t, err, "hello")
			}
			require.Less(t, time.Since(start), 2*time.Second)
			require.Nil(t, d.proc)
			pid := readPID(t, pidFile)
			require.True(t, processGone(pid), "child %d survives", pid)

			// The next Detect starts a new, healthy child.
			t.Setenv(helperEnv, "ok")
			t.Setenv(helperChildPID, "")
			_, err = d.Detect(ctx, tinyFrame())
			require.NoError(t, err)
			require.NotNil(t, d.proc)
			start = time.Now()
			require.NoError(t, d.Close())
			require.Less(t, time.Since(start), 3*time.Second)
		})
	}
}

// A grandchild holding the child's stdout and stderr open must neither
// survive the kill nor keep Wait blocked.
func TestProcDetectorKillsGrandchild(t *testing.T) {
	pidFile := t.TempDir() + "/pid"
	t.Setenv(helperPIDFile, pidFile)
	d := helperDetector(t, "grandchild")
	start := time.Now()
	_, err := d.Detect(context.Background(), tinyFrame())
	require.ErrorContains(t, err, "hello timed out")
	require.Less(t, time.Since(start), 2500*time.Millisecond)
	pid := readPID(t, pidFile)
	waitRealIO(t, func() bool { return processGone(pid) }, "grandchild exit")
	require.Nil(t, d.proc)
}

// Cancelling the context interrupts a pending read.
//
//testpolicy:realtime real OCR worker subprocess, bounded by a context deadline
func TestProcDetectorContextCancel(t *testing.T) {
	d := helperDetector(t, "silent")
	d.reqTimeout = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := d.Detect(ctx, tinyFrame())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 2*time.Second)
	require.Nil(t, d.proc)
}

//testpolicy:realtime subprocess exit and worker dump files are real kernel I/O
func waitRealIO(t *testing.T, ready func() bool, what string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-ticker.C:
		}
	}
}

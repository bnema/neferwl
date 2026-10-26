package hints

import (
	"context"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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

// pidFifo makes a FIFO the helper writes a pid to. The test holds it
// read-write, so the helper's write never blocks; await reads the pid.
func pidFifo(t *testing.T) (path string, await func() int) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "pid")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return path, func() int {
		t.Helper()
		if err := f.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var buf [32]byte
		n, err := f.Read(buf[:])
		if err != nil {
			t.Fatalf("child PID: %v", err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
		if err != nil {
			t.Fatalf("child PID %q: %v", buf[:n], err)
		}
		return pid
	}
}

// waitGone waits until pid exited (a zombie counts), within one deadline,
// through a pidfd that polls readable at exit.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	fd, err := unix.PidfdOpen(pid, 0)
	if err == unix.ESRCH {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, 5000)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("child %d survives", pid)
		}
		return
	}
}

func TestProcDetectorHungChildIsKilledAndRestarted(t *testing.T) {
	for _, mode := range []string{"nohello", "silent"} {
		t.Run(mode, func(t *testing.T) {
			pidFile, readPID := pidFifo(t)
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
			waitGone(t, readPID())

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
	pidFile, readPID := pidFifo(t)
	t.Setenv(helperPIDFile, pidFile)
	d := helperDetector(t, "grandchild")
	start := time.Now()
	_, err := d.Detect(context.Background(), tinyFrame())
	require.ErrorContains(t, err, "hello timed out")
	require.Less(t, time.Since(start), 2500*time.Millisecond)
	waitGone(t, readPID())
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

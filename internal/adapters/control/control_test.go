package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/workspaceid"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

//testpolicy:realtime Unix socket exchanges and server shutdown need bounded waits outside synctest.
func TestProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session", "control.sock")
	s, err := Listen(path, workspaceid.WithPrefix("test"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for path, want := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("permissions %s: %v %v", path, info, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan ports.ControlRequest)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	exchange := func(line string) map[string]json.RawMessage {
		t.Helper()
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
		data, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	hello := exchange(`{"id":"hello","op":"hello"}`)
	if string(hello["id"]) != `"hello"` || string(hello["ok"]) != "true" || string(hello["v"]) != "1" {
		t.Fatal(hello)
	}
	// A line that is not JSON has no id to echo: null.
	if bad := exchange(`{broken`); string(bad["id"]) != "null" || string(bad["error"]) != `"bad-request"` {
		t.Fatal(bad)
	}
	if long := exchange(`{"id":9,"op":"do","action":"none","timeout":"6m"}`); string(long["detail"]) != `"timeout exceeds 5m"` {
		t.Fatal(long)
	}
	// The id is optional and echoed as null.
	if anon := exchange(`{"op":"hello"}`); string(anon["id"]) != "null" || string(anon["ok"]) != "true" {
		t.Fatal(anon)
	}
	var commands []command
	if err := json.Unmarshal(hello["commands"], &commands); err != nil {
		t.Fatal(err)
	}
	if len(commands) != len(domain.Commands()) || commands[0].Name != domain.Commands()[0].Name || len(commands[0].Caps) == 0 {
		t.Fatal(commands)
	}
	for i, spec := range domain.Commands() {
		if commands[i].Kind != spec.Kind.String() {
			t.Errorf("command %q kind = %q, want %q", spec.Name, commands[i].Kind, spec.Kind.String())
		}
	}
	var caps []string
	if err := json.Unmarshal(hello["caps"], &caps); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(caps, []string{"read", "act", "input", "capture", "admin"}) {
		t.Fatal(caps)
	}
	for _, line := range []string{`{broken`, `{"id":2,"op":"unknown"}`, `{"id":3,"op":"do","action":"none","wait":"bad"}`, `{"id":4,"op":"do","action":"none","until":"bad"}`, `{"id":5,"op":"do","action":"none","timeout":"bad"}`, `{"id":5,"op":"do","action":"none","timeout":"6m"}`, `{"op":"do"}`} {
		got := exchange(line)
		if string(got["error"]) != `"bad-request"` || string(got["ok"]) != "false" {
			t.Fatal(got)
		}
	}
	coreDone := make(chan struct{})
	go func() {
		defer close(coreDone)
		for i := 0; i < 3; i++ {
			select {
			case message := <-out:
				switch req := message.(type) {
				case ports.ControlDo:
					if req.Action != "none" || req.Caps != domain.CapsAll || cap(req.Reply) != 1 {
						t.Errorf("do: %+v", req)
					}
					if i == 0 {
						if req.Wait != domain.WaitPresented || req.Timeout != 2*time.Second || req.Until == nil || req.Until.Kind != domain.UntilFocused || req.Until.AppID != "foot" {
							t.Errorf("explicit: %+v", req)
						}
					} else if req.Wait != domain.DefaultWait || req.Timeout != 10*time.Second || req.Until != nil {
						t.Errorf("defaults: %+v", req)
					}
					// Core may reach less than the requested level; preserve its report.
					timedOut := ""
					if i == 1 {
						timedOut = ports.ControlErrTimeout
					}
					req.Reply <- ports.ControlReply{Done: domain.WaitCommitted, Err: timedOut, Diff: domain.StateDiff{FocusFrom: 1, FocusTo: 2, FocusChanged: true}, Late: []domain.WindowID{3}}
				case ports.ControlQuery:
					if req.What != "state" || req.Caps != domain.CapsAll || cap(req.Reply) != 1 {
						t.Errorf("query: %+v", req)
					}
					req.Reply <- ports.ControlReply{Done: domain.WaitCommitted, State: domain.State{Outputs: []domain.OutputState{{Name: "A", WorkspaceID: 7}}}}
				default:
					t.Errorf("unexpected %T", message)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	got := exchange(`{"id":6,"op":"do","action":"none","wait":"presented","until":"focused app_id=foot","timeout":"2s"}`)
	if string(got["id"]) != "6" || string(got["done"]) != `"committed"` || string(got["ok"]) != "true" {
		t.Fatal(got)
	}
	if _, present := got["error"]; present {
		t.Fatalf("successful reply includes error: %s", got["error"])
	}
	var d map[string]any
	if err := json.Unmarshal(got["diff"], &d); err != nil {
		t.Fatal(err)
	}
	if d["focus_from"] != float64(1) || d["focus_to"] != float64(2) || d["focus_changed"] != true {
		t.Fatal(d)
	}
	got = exchange(`{"id":7,"op":"do","action":"none"}`)
	if string(got["error"]) != `"timeout"` || string(got["late"]) != "[3]" {
		t.Fatal(got)
	}
	got = exchange(`{"id":8,"op":"query","what":"state"}`)
	var state struct {
		Outputs []struct {
			WorkspaceID string `json:"workspace_id"`
		}
	}
	if err := json.Unmarshal(got["state"], &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Outputs) != 1 || state.Outputs[0].WorkspaceID != "test-7" {
		t.Fatal(state)
	}
	<-coreDone
	// Leave one request awaiting core forever; cancellation must release it.
	if _, err := conn.Write([]byte("{\"id\":9,\"op\":\"query\",\"what\":\"state\"}\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out:
	case <-time.After(10 * time.Second):
		t.Fatal("request not received")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestListenPreservesLiveSocketAndFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	ids := workspaceid.WithPrefix("test")
	s, err := Listen(path, ids, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if other, err := Listen(path, ids, Options{}); err == nil {
		other.Close()
		t.Fatal("replaced live listener")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if other, err := Listen(path, ids, Options{}); err == nil {
		other.Close()
		t.Fatal("replaced regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatal(string(data), err)
	}
}

func TestListenRemovesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Listen(path, workspaceid.WithPrefix("test"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

//testpolicy:realtime Socket reads and cancellation completion are bounded real I/O waits.
func TestConnectionOrderingAndClose(t *testing.T) {
	s, err := Listen(filepath.Join(t.TempDir(), "control.sock"), workspaceid.WithPrefix("test"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan ports.ControlRequest, 2)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	conn, err := net.Dial("unix", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("{\"id\":1,\"op\":\"query\",\"what\":\"state\"}\n{\"id\":2,\"op\":\"query\",\"what\":\"state\"}\n")); err != nil {
		t.Fatal(err)
	}
	var first ports.ControlQuery
	select {
	case req := <-out:
		first = req.(ports.ControlQuery)
	case <-time.After(10 * time.Second):
		t.Fatal("first request missing")
	}
	// A second connection's hello is an I/O barrier while the first awaits core.
	barrier, err := net.Dial("unix", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Close()
	if err := barrier.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := barrier.Write([]byte("{\"id\":0,\"op\":\"hello\"}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(barrier).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out:
		t.Fatal("read next request before core replied")
	default:
	}
	first.Reply <- ports.ControlReply{}
	reader := bufio.NewReader(conn)
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-out:
		req.(ports.ControlQuery).Reply <- ports.ControlReply{}
	case <-time.After(10 * time.Second):
		t.Fatal("second request missing")
	}
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	// Both handlers now block reading; Close must release them and the accept loop.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not stop Run")
	}
}

//testpolicy:realtime A real Unix socket verifies the reply is written before shutdown cancellation.
func TestQuitReplyBeforeShutdown(t *testing.T) {
	s, err := Listen(filepath.Join(t.TempDir(), "control.sock"), workspaceid.WithPrefix("quit"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	out := make(chan ports.ControlRequest)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	conn, err := net.Dial("unix", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("{\"op\":\"do\",\"action\":\"quit\"}\n")); err != nil {
		t.Fatal(err)
	}
	var req ports.ControlDo
	select {
	case message := <-out:
		req = message.(ports.ControlDo)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if req.Wait != domain.DefaultWait {
		t.Fatalf("wait: %v", req.Wait)
	}
	req.Reply <- ports.ControlReply{Done: domain.WaitCommitted}
	s.WaitQuitReplies(ctx)
	cancel()
	var response reply
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Done != "committed" {
		t.Fatalf("reply: %+v", response)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

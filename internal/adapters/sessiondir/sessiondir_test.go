package sessiondir

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistry(t *testing.T) {
	runtime := t.TempDir()
	for _, id := range []string{"b", "a"} {
		d, err := Create(runtime, id)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		meta := Meta{ID: id, Control: d.Path() + "/control.sock", PID: os.Getpid(), Created: time.Now()}
		if err := d.WriteMeta(meta); err != nil {
			t.Fatal(err)
		}
		if d.Meta() != meta {
			t.Fatal(d.Meta())
		}
		if _, err := Create(runtime, id); err == nil {
			t.Fatal("duplicate live id accepted")
		}
		st, err := os.Stat(d.Path())
		if err != nil || st.Mode().Perm() != 0700 {
			t.Fatal(st, err)
		}
	}
	metas, err := List(runtime)
	if err != nil || len(metas) != 2 || metas[0].ID != "a" || metas[1].ID != "b" {
		t.Fatal(metas, err)
	}
	stale := filepath.Join(Root(runtime), "stale")
	if err := os.Mkdir(stale, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "old"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	d, err := Create(runtime, "stale")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stale, "old")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Simulate process exit: release the lifetime lock without cleanup.
	if err := unlock(d.lock); err != nil {
		t.Fatal(err)
	}
	d.lock = nil
	if _, err := List(runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	d, err = Create(runtime, "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.Path()); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
func TestIDs(t *testing.T) {
	for _, id := range []string{"", "-a", "A", "../a", "a/b", "a_b", "abcdefghijklmnopqrstuvwxyz1234567"} {
		if ValidID(id) {
			t.Fatal(id)
		}
		if _, err := Create(t.TempDir(), id); err == nil {
			t.Fatal(id)
		}
	}
	for _, id := range []string{"a", "0", "a-b", "abcdefghijklmnopqrstuvwxyz123456", NewID()} {
		if !ValidID(id) {
			t.Fatal(id)
		}
	}
	if _, err := Create("", "a"); err == nil {
		t.Fatal("empty runtime accepted")
	}
}

// Close removes the directory under the registry lock; the id is then free
// for a new Create, and the registry lock was released.
func TestCloseThenRecreate(t *testing.T) {
	runtime := t.TempDir()
	for range 2 {
		d, err := Create(runtime, "again")
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(d.Path()); !os.IsNotExist(err) {
			t.Fatalf("directory kept: %v", err)
		}
	}
	registry, err := registryLock(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := unlock(registry); err != nil {
		t.Fatal(err)
	}
}

// Package sessiondir owns the live-session registry in the runtime directory.
package sessiondir

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

type Meta struct {
	ID      string    `json:"id"`
	Wayland string    `json:"wayland"`
	Display string    `json:"display"`
	Control string    `json:"control"`
	State   string    `json:"state"`
	Profile string    `json:"profile"`
	PID     int       `json:"pid"`
	Created time.Time `json:"created"`
}

// Dir holds the lifetime lock. Its methods are called by its owner serially.
type Dir struct {
	path string
	meta Meta
	lock *os.File
}

func (d *Dir) Path() string         { return d.path }
func (d *Dir) Meta() Meta           { return d.meta }
func Root(runtimeDir string) string { return filepath.Join(runtimeDir, "neferwl", "sessions") }
func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 32 {
		return false
	}
	for i, c := range id {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && c == '-' {
			continue
		}
		return false
	}
	return true
}
func NewID() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "s-" + hex.EncodeToString(b[:])
}

// registryLock serializes reclamation against creation, so nobody can take a
// lifetime lock on an inode another registry operation is about to remove.
func registryLock(runtimeDir string) (*os.File, error) {
	if runtimeDir == "" {
		return nil, errors.New("XDG_RUNTIME_DIR must be set")
	}
	if err := os.MkdirAll(Root(runtimeDir), 0700); err != nil {
		return nil, err
	}
	return lockRegistryFile(Root(runtimeDir))
}

// lockRegistryFile takes the registry lock of an existing sessions root.
func lockRegistryFile(root string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func unlock(f *os.File) error {
	a := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return errors.Join(a, f.Close())
}
func openLock(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(filepath.Join(path, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()
		return nil, true, nil
	}
	if err != nil {
		f.Close()
		return nil, false, err
	}
	return f, false, nil
}
func Create(runtimeDir, id string) (*Dir, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	registry, err := registryLock(runtimeDir)
	if err != nil {
		return nil, err
	}
	defer unlock(registry)
	path := filepath.Join(Root(runtimeDir), id)
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	lock, live, err := openLock(path)
	if err != nil {
		return nil, err
	}
	if live {
		return nil, fmt.Errorf("session %q is live", id)
	}
	// Keep the locked inode in place while clearing stale contents.
	entries, err := os.ReadDir(path)
	if err != nil {
		unlock(lock)
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() != "lock" {
			if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
				unlock(lock)
				return nil, err
			}
		}
	}
	if err := os.Chmod(path, 0700); err != nil {
		unlock(lock)
		return nil, err
	}
	return &Dir{path: path, lock: lock, meta: Meta{ID: id}}, nil
}
func (d *Dir) WriteMeta(meta Meta) error {
	if d.lock == nil {
		return errors.New("session directory is closed")
	}
	if meta.ID != filepath.Base(d.path) {
		return errors.New("metadata session id does not match directory")
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp := filepath.Join(d.path, "meta.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(d.path, "meta.json")); err != nil {
		return err
	}
	d.meta = meta
	return nil
}
func (d *Dir) Close() error {
	if d.lock == nil {
		return nil
	}
	// Retain the lifetime lock until the directory is gone, and hold the
	// registry lock so a same-id Create cannot lock the inode being removed.
	registry, regErr := lockRegistryFile(filepath.Dir(d.path))
	err := os.RemoveAll(d.path)
	err = errors.Join(regErr, err, unlock(d.lock))
	if registry != nil {
		err = errors.Join(err, unlock(registry))
	}
	d.lock = nil
	return err
}
func List(runtimeDir string) ([]Meta, error) {
	registry, err := registryLock(runtimeDir)
	if err != nil {
		return nil, err
	}
	defer unlock(registry)
	entries, err := os.ReadDir(Root(runtimeDir))
	if err != nil {
		return nil, err
	}
	var metas []Meta
	for _, entry := range entries {
		if !entry.IsDir() || !ValidID(entry.Name()) {
			continue
		}
		path := filepath.Join(Root(runtimeDir), entry.Name())
		lock, live, err := openLock(path)
		if err != nil {
			return nil, err
		}
		if !live {
			err := os.RemoveAll(path)
			err = errors.Join(err, unlock(lock))
			if err != nil {
				return nil, err
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, "meta.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var meta Meta
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, err
		}
		if meta.ID != entry.Name() {
			return nil, fmt.Errorf("session metadata id mismatch: %s", path)
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].ID < metas[j].ID })
	return metas, nil
}

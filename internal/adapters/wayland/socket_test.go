package wayland

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestListen(t *testing.T) {
	dir := t.TempDir()
	n1, fd1, close1, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer close1()
	defer unix.Close(fd1)
	n2, fd2, close2, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd2)
	if n1 != "wayland-1" || n2 != "wayland-2" {
		t.Fatalf("names: %s, %s", n1, n2)
	}
	close2()
	for _, suffix := range []string{"", ".lock"} {
		if _, err := os.Stat(filepath.Join(dir, n2+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", n2+suffix, err)
		}
	}
	close1()
	for _, suffix := range []string{"", ".lock"} {
		if _, err := os.Stat(filepath.Join(dir, n1+suffix)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", n1+suffix, err)
		}
	}
}

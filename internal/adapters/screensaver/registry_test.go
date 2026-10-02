package screensaver

import "testing"

func TestRegistry(t *testing.T) {
	r := newRegistry()
	a1, first, ok := r.inhibit(":1.1")
	if !ok || !first || !r.held() {
		t.Fatal("first inhibit")
	}
	a2, first, _ := r.inhibit(":1.1")
	if first || a2 == a1 {
		t.Fatalf("second inhibit: first %v, cookies %d %d", first, a1, a2)
	}
	b, first, _ := r.inhibit(":1.2")
	if !first {
		t.Fatal("other sender not first")
	}
	// Another connection cannot release a cookie it does not own.
	if r.uninhibit(":1.2", a1) || r.uninhibit(":1.1", 999) {
		t.Fatal("foreign or unknown cookie released")
	}
	if !r.uninhibit(":1.1", a1) || !r.held() {
		t.Fatal("uninhibit")
	}
	r.drop(":1.1")
	if !r.held() {
		t.Fatal("drop removed another sender's cookie")
	}
	if !r.uninhibit(":1.2", b) || r.held() {
		t.Fatal("still held")
	}
	if _, first, _ := r.inhibit(":1.1"); !first {
		t.Fatal("dropped sender is not first again")
	}
}

func TestRegistryBoundsSender(t *testing.T) {
	r := newRegistry()
	for range maxPerSender {
		if _, _, ok := r.inhibit(":1.1"); !ok {
			t.Fatal("refused under the bound")
		}
	}
	if _, _, ok := r.inhibit(":1.1"); ok {
		t.Fatal("accepted over the bound")
	}
	if _, _, ok := r.inhibit(":1.2"); !ok {
		t.Fatal("bound is per sender")
	}
}

func TestRegistryCookieWrapSkipsZeroAndUsed(t *testing.T) {
	r := newRegistry()
	r.next = ^uint32(0) - 1
	c1, _, _ := r.inhibit(":1.1") // max
	c2, _, _ := r.inhibit(":1.1") // wraps past 0
	if c1 != ^uint32(0) || c2 != 1 {
		t.Fatalf("cookies %d %d", c1, c2)
	}
	r.next = 0
	if c3, _, _ := r.inhibit(":1.1"); c3 != 2 {
		t.Fatalf("reused cookie %d", c3)
	}
}

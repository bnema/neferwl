package screensaver

// maxPerSender bounds the cookies one D-Bus connection may hold, so a
// looping client cannot grow the registry without limit.
const maxPerSender = 64

// registry is the inhibitions in effect: cookie → owning connection. The
// session stays awake while it is not empty. It is owned by Run's goroutine.
type registry struct {
	next    uint32
	cookies map[uint32]string
	counts  map[string]int
}

func newRegistry() *registry {
	return &registry{cookies: map[uint32]string{}, counts: map[string]int{}}
}

// inhibit adds a cookie for sender. ok is false when sender holds too many;
// first is set when sender had none, so the caller checks it is still on
// the bus (it may have left before its call was handled).
func (r *registry) inhibit(sender string) (cookie uint32, first, ok bool) {
	if r.counts[sender] >= maxPerSender {
		return 0, false, false
	}
	for {
		r.next++
		if _, used := r.cookies[r.next]; r.next != 0 && !used {
			break
		}
	}
	r.cookies[r.next] = sender
	r.counts[sender]++
	return r.next, r.counts[sender] == 1, true
}

// uninhibit removes cookie if sender owns it: one client cannot release
// another's inhibition.
func (r *registry) uninhibit(sender string, cookie uint32) bool {
	if owner, ok := r.cookies[cookie]; !ok || owner != sender {
		return false
	}
	delete(r.cookies, cookie)
	if r.counts[sender]--; r.counts[sender] == 0 {
		delete(r.counts, sender)
	}
	return true
}

// drop removes every cookie of a connection that left the bus.
func (r *registry) drop(sender string) {
	if r.counts[sender] == 0 {
		return
	}
	for cookie, owner := range r.cookies {
		if owner == sender {
			delete(r.cookies, cookie)
		}
	}
	delete(r.counts, sender)
}

func (r *registry) held() bool { return len(r.cookies) > 0 }

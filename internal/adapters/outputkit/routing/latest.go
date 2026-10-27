package routing

// Latest sends without blocking, replacing an unread value and reporting whether
// one was dropped. The channel must have a single sender.
func Latest[T any](ch chan T, v T) (replaced bool) {
	select {
	case ch <- v:
	default:
		select {
		case <-ch:
			replaced = true
		default:
		}
		select {
		case ch <- v:
		default:
		}
	}
	return replaced
}

package routing

func latest[T any](ch chan T, v T) (replaced bool) {
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

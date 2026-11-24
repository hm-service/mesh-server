package httpx

// Gate bounds how many requests of one kind run at the same time. Waiting is
// not part of it: a request that finds the gate full is answered right away, so
// a slow dependency cannot pile up an unbounded queue.
type Gate struct {
	slots chan struct{}
}

// NewGate returns a gate with n slots.
func NewGate(n int) *Gate {
	return &Gate{slots: make(chan struct{}, n)}
}

// Acquire takes a slot and reports whether it got one.
func (g *Gate) Acquire() bool {
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release returns the slot taken by Acquire.
func (g *Gate) Release() {
	<-g.slots
}

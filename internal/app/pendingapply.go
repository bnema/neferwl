package app

import (
	"errors"
	"fmt"
)

// applyProgress belongs exclusively to the output loop. An instance is identified
// by its Ready channel, which is distinct for each start of a connector.
type applyProgress struct {
	id        uint64
	active    bool
	required  map[string]bool
	waiting   map[string]bool // stopped instances awaiting replacement or removal
	ready     map[string]<-chan error
	completed map[<-chan error]bool
	err       error
}

type applyDecision struct {
	reply    bool
	err      error
	rollback bool
}

func (p *applyProgress) start(id uint64, required map[string]bool, stopping map[string]bool) applyDecision {
	p.id, p.active, p.err = id, true, nil
	p.required = required
	p.waiting = map[string]bool{}
	p.ready = map[string]<-chan error{}
	for name := range stopping {
		p.waiting[name] = true
	}
	return applyDecision{}
}

func (p *applyProgress) stopping(name string) {
	if p.active {
		p.waiting[name] = true
		delete(p.ready, name)
	}
}

func (p *applyProgress) started(name string, instance <-chan error) {
	if p.active && p.required[name] && !p.completed[instance] && !p.waiting[name] {
		p.ready[name] = instance
	}
}

func (p *applyProgress) readyEvent(name string, instance <-chan error, err error) applyDecision {
	if p.completed == nil {
		p.completed = map[<-chan error]bool{}
	}
	if err == nil {
		p.completed[instance] = true
	}
	if p.active && p.ready[name] == instance {
		delete(p.ready, name)
		if err != nil {
			return p.fail(fmt.Errorf("%s: %w", name, err))
		}
	} else if p.active && err != nil && p.required[name] && !p.waiting[name] {
		return p.fail(fmt.Errorf("%s: %w", name, err))
	}
	return p.complete()
}

func (p *applyProgress) stopped(name string, instance <-chan error, restart bool, err error) applyDecision {
	delete(p.completed, instance)
	if !p.active {
		return applyDecision{}
	}
	if p.ready[name] == instance {
		delete(p.ready, name)
	}
	if err != nil {
		return p.fail(fmt.Errorf("%s stopped: %w", name, err))
	}
	if restart && p.required[name] {
		p.waiting[name] = false
		return applyDecision{}
	} else {
		delete(p.waiting, name)
		if p.required[name] {
			return p.fail(fmt.Errorf("%s stopped before ready", name))
		}
	}
	return p.complete()
}

func (p *applyProgress) scanned(running map[string]<-chan error) applyDecision {
	if !p.active {
		return applyDecision{}
	}
	for name := range p.required {
		instance := running[name]
		if instance == nil {
			return p.fail(fmt.Errorf("%s failed to start", name))
		}
		if stopping, ok := p.waiting[name]; ok {
			if stopping {
				continue
			}
			delete(p.waiting, name)
		}
		if !p.completed[instance] {
			p.ready[name] = instance
		} else {
			delete(p.ready, name)
		}
	}
	return p.complete()
}

// scanError fails the active operation even if an old output was already ready.
func (p *applyProgress) scanError(err error) applyDecision {
	if !p.active || err == nil {
		return applyDecision{}
	}
	return p.fail(err)
}

func (p *applyProgress) timeout(id uint64) applyDecision {
	if !p.active || id != p.id {
		return applyDecision{}
	}
	return p.fail(errTimeout)
}

func (p *applyProgress) fail(err error) applyDecision {
	p.err = errors.Join(p.err, err)
	p.active = false
	return applyDecision{reply: true, err: p.err, rollback: true}
}

func (p *applyProgress) complete() applyDecision {
	if !p.active || len(p.waiting) != 0 || len(p.ready) != 0 {
		return applyDecision{}
	}
	p.active = false
	return applyDecision{reply: true, err: p.err, rollback: p.err != nil}
}

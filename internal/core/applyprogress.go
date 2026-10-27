package core

import (
	"errors"
	"fmt"

	"github.com/bnema/neferwl/internal/domain"
)

var ErrApplyTimeout = errors.New("timeout")

// ApplyProgress tracks whether the outputs of a DRM configuration are ready.
// It belongs exclusively to the output loop. An instance is identified by its
// output instance ID, which is distinct for each start of a connector.
type ApplyProgress struct {
	id        uint64
	Active    bool
	Required  map[string]bool
	waiting   map[string]bool // stopped instances awaiting replacement or removal
	Ready     map[string]domain.OutputInstance
	completed map[domain.OutputInstance]bool
	Err       error
}

// ApplyDecision reports whether the configuration is finished and its result.
type ApplyDecision struct {
	Reply bool
	Err   error
}

func (p *ApplyProgress) Start(id uint64, required map[string]bool, stopping map[string]bool) ApplyDecision {
	p.id, p.Active, p.Err = id, true, nil
	p.Required = required
	p.waiting = map[string]bool{}
	p.Ready = map[string]domain.OutputInstance{}
	for name := range stopping {
		p.waiting[name] = true
	}
	return ApplyDecision{}
}

func (p *ApplyProgress) Stopping(name string) {
	if p.Active {
		p.waiting[name] = true
		delete(p.Ready, name)
	}
}

func (p *ApplyProgress) Started(name string, instance domain.OutputInstance) {
	if p.Active && p.Required[name] && !p.completed[instance] && !p.waiting[name] {
		p.Ready[name] = instance
	}
}

// readyEvent records a readiness result. current is the connector's running
// instance: results of a stopped instance are ignored.
func (p *ApplyProgress) ReadyEvent(name string, instance, current domain.OutputInstance, err error) ApplyDecision {
	if instance != current {
		return ApplyDecision{}
	}
	if p.completed == nil {
		p.completed = map[domain.OutputInstance]bool{}
	}
	if err == nil {
		p.completed[instance] = true
	}
	if p.Active && p.Ready[name] == instance {
		delete(p.Ready, name)
		if err != nil {
			return p.fail(fmt.Errorf("%s: %w", name, err))
		}
	} else if p.Active && err != nil && p.Required[name] && !p.waiting[name] {
		return p.fail(fmt.Errorf("%s: %w", name, err))
	}
	return p.complete()
}

func (p *ApplyProgress) Stopped(name string, instance domain.OutputInstance, restart bool, err error) ApplyDecision {
	delete(p.completed, instance)
	if !p.Active {
		return ApplyDecision{}
	}
	if p.Ready[name] == instance {
		delete(p.Ready, name)
	}
	if err != nil {
		return p.fail(fmt.Errorf("%s stopped: %w", name, err))
	}
	if restart && p.Required[name] {
		p.waiting[name] = false
		return ApplyDecision{}
	} else {
		delete(p.waiting, name)
		if p.Required[name] {
			return p.fail(fmt.Errorf("%s stopped before ready", name))
		}
	}
	return p.complete()
}

func (p *ApplyProgress) Scanned(running map[string]domain.OutputInstance) ApplyDecision {
	if !p.Active {
		return ApplyDecision{}
	}
	for name := range p.Required {
		instance := running[name]
		if instance == 0 {
			return p.fail(fmt.Errorf("%s failed to start", name))
		}
		if stopping, ok := p.waiting[name]; ok {
			if stopping {
				continue
			}
			delete(p.waiting, name)
		}
		if !p.completed[instance] {
			p.Ready[name] = instance
		} else {
			delete(p.Ready, name)
		}
	}
	return p.complete()
}

// scanError fails the active operation even if an old output was already ready.
func (p *ApplyProgress) ScanError(err error) ApplyDecision {
	if !p.Active || err == nil {
		return ApplyDecision{}
	}
	return p.fail(err)
}

func (p *ApplyProgress) Timeout(id uint64) ApplyDecision {
	if !p.Active || id != p.id {
		return ApplyDecision{}
	}
	return p.fail(ErrApplyTimeout)
}

func (p *ApplyProgress) fail(err error) ApplyDecision {
	p.Err = errors.Join(p.Err, err)
	p.Active = false
	return ApplyDecision{Reply: true, Err: p.Err}
}

func (p *ApplyProgress) complete() ApplyDecision {
	if !p.Active || len(p.waiting) != 0 || len(p.Ready) != 0 {
		return ApplyDecision{}
	}
	p.Active = false
	return ApplyDecision{Reply: true, Err: p.Err}
}

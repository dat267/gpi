package facets

import (
	"fmt"
	"sync"
)

// Port of the FacetLifecycle state machine in src/facets/host.ts.

// Lifecycle states.
const (
	lifecycleSettingUp = "setting_up"
	lifecyclePrepared  = "prepared"
	lifecycleActive    = "active"
	lifecycleDisposing = "disposing"
	lifecycleDead      = "dead"
)

// facetLifecycle owns one facet's effects, observations, and activation
// callbacks.
type facetLifecycle struct {
	mu           sync.Mutex
	id           string
	effects      []func() error
	observations []func() (func(), error)
	activations  []func() error
	state        string
	serviceAcess bool
}

func newFacetLifecycle(id string) *facetLifecycle {
	return &facetLifecycle{id: id, state: lifecycleSettingUp}
}

func (l *facetLifecycle) assertSettingUp(operation string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != lifecycleSettingUp {
		return fmt.Errorf("Facet %s can %s only during setup", l.id, operation)
	}
	return nil
}

func (l *facetLifecycle) assertRunning(operation string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != lifecycleSettingUp && l.state != lifecycleActive {
		return fmt.Errorf("Facet %s cannot %s while %s", l.id, operation, l.state)
	}
	return nil
}

func (l *facetLifecycle) assertActive(operation string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != lifecycleActive {
		return fmt.Errorf("Facet %s can %s only while active", l.id, operation)
	}
	return nil
}

// assertServiceAccess guards handle use (upstream assertServiceAccess).
func (l *facetLifecycle) assertServiceAccess() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.serviceAcess {
		return fmt.Errorf("Facet %s service handles cannot be used while %s", l.id, l.state)
	}
	return nil
}

// revoke disables handle use without changing the state.
func (l *facetLifecycle) revoke() {
	l.mu.Lock()
	l.serviceAcess = false
	l.mu.Unlock()
}

func (l *facetLifecycle) own(disposal func() error) error {
	if err := l.assertRunning("own resources"); err != nil {
		return err
	}
	l.mu.Lock()
	l.effects = append(l.effects, disposal)
	l.mu.Unlock()
	return nil
}

func (l *facetLifecycle) observe(start func() (func(), error)) error {
	if err := l.assertSettingUp("observe services"); err != nil {
		return err
	}
	l.mu.Lock()
	l.observations = append(l.observations, start)
	l.mu.Unlock()
	return nil
}

func (l *facetLifecycle) onActivate(callback func() error) error {
	if err := l.assertSettingUp("register activation callbacks"); err != nil {
		return err
	}
	l.mu.Lock()
	l.activations = append(l.activations, callback)
	l.mu.Unlock()
	return nil
}

func (l *facetLifecycle) prepared() error {
	if err := l.assertSettingUp("finish setup"); err != nil {
		return err
	}
	l.mu.Lock()
	l.state = lifecyclePrepared
	l.mu.Unlock()
	return nil
}

// activate starts observations, then runs activation callbacks in order.
func (l *facetLifecycle) activate() error {
	l.mu.Lock()
	if l.state != lifecyclePrepared {
		l.mu.Unlock()
		return fmt.Errorf("Facet %s is not prepared", l.id)
	}
	l.state = lifecycleActive
	l.serviceAcess = true
	observations := append([]func() (func(), error){}, l.observations...)
	callbacks := append([]func() error{}, l.activations...)
	l.mu.Unlock()

	for _, start := range observations {
		stop, err := start()
		if err != nil {
			return err
		}
		l.mu.Lock()
		l.effects = append(l.effects, func() error {
			stop()
			return nil
		})
		l.mu.Unlock()
	}
	for _, callback := range callbacks {
		if err := callback(); err != nil {
			return err
		}
	}
	return nil
}

// dispose runs effects in reverse order and settles the lifecycle as dead.
func (l *facetLifecycle) dispose() error {
	l.mu.Lock()
	if l.state == lifecycleDead {
		l.mu.Unlock()
		return nil
	}
	l.state = lifecycleDisposing
	effects := l.effects
	l.effects = nil
	l.observations = nil
	l.activations = nil
	l.mu.Unlock()

	var collected []error
	for index := len(effects) - 1; index >= 0; index-- {
		if err := effects[index](); err != nil {
			collected = append(collected, err)
		}
	}

	l.mu.Lock()
	l.serviceAcess = false
	l.state = lifecycleDead
	l.mu.Unlock()
	return collectErrors(collected, fmt.Sprintf("Failed to dispose facet %s", l.id))
}

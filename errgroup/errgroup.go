// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package errgroup provides synchronization, error propagation, and Context
// cancelation for groups of goroutines working on subtasks of a common task.
//
// [errgroup.Group] is related to [sync.WaitGroup] but adds handling of tasks
// returning errors.
package errgroup

import (
	"context"
	"fmt"
	"sync"
)

type token struct{}

// A Group is a collection of goroutines working on subtasks that are part of
// the same overall task.
//
// A zero Group is valid, has no limit on the number of active goroutines,
// and does not cancel on error.
type Group struct {
	cancel func(error)

	wg sync.WaitGroup

	sem chan token

	errOnce sync.Once
	err     error
}

func (g *Group) done() {
	if g.sem != nil {
		<-g.sem
	}
	g.wg.Done()
}

// WithContext returns a new Group and an associated Context derived from ctx.
//
// The derived Context is canceled the first time a function passed to Go
// returns a non-nil error or the first time Wait returns, whichever occurs
// first.
func WithContext(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := withCancelCause(ctx)
	return &Group{cancel: cancel}, ctx
}

// Wait blocks until all function calls from the Go method have returned, then
// returns the first non-nil error (if any) from them.
func (g *Group) Wait() error {
	g.wg.Wait()
	if g.cancel != nil {
		g.cancel(g.err)
	}
	return g.err
}

// Go calls the given function in a new goroutine.
// It blocks until the new goroutine can be added without the number of
// active goroutines in the group exceeding the configured limit.
//
// The first call to return a non-nil error cancels the group's context, if the
// group was created by calling WithContext. The error will be returned by Wait.
func (g *Group) Go(f func() error) {
	g.do(f, nil, false)
}

// GoChan is like Go, but returns a channel that is closed after f has returned.
func (g *Group) GoChan(f func() error) <-chan struct{} {
	ch := make(chan struct{})
	g.do(f, ch, false)
	return ch
}

// TryGo calls the given function in a new goroutine only if the number of
// active goroutines in the group is currently below the configured limit.
//
// The return value reports whether the goroutine was started.
func (g *Group) TryGo(f func() error) bool {
	return g.do(f, nil, true)
}

// TryGoChan is like TryGo but, if f was started, it returns a non-nil channel like GoChan.
func (g *Group) TryGoChan(f func() error) <-chan struct{} {
	ch := make(chan struct{})
	if g.do(f, ch, true) {
		return ch
	}
	return nil
}

func (g *Group) do(f func() error, ch chan struct{}, nb bool) bool {
	if g.sem != nil {
		if nb {
			select {
			case g.sem <- token{}:
				// Note: this allows barging iff channels in general allow barging.
			default:
				return false
			}
		} else {
			g.sem <- token{}
		}
	}
	
	g.wg.Add(1)
	go func() {
		defer func() {
			if ch != nil {
				// This must happen *after* the call to g.cancel, if any.
				close(ch) 
			}
			g.done()
		}()

		if err := f(); err != nil {
			g.errOnce.Do(func() {
				g.err = err
				if cancel := g.cancel; cancel != nil {
					g.cancel = nil
					cancel(g.err)
				}
			})
		}
	}()

	return true
}

// SetLimit limits the number of active goroutines in this group to at most n.
// A negative value indicates no limit.
//
// Any subsequent call to the Go method will block until it can add an active
// goroutine without exceeding the configured limit.
//
// The limit must not be modified while any goroutines in the group are active.
func (g *Group) SetLimit(n int) {
	if n < 0 {
		g.sem = nil
		return
	}
	if len(g.sem) != 0 {
		panic(fmt.Errorf("errgroup: modify limit while %v goroutines in the group are still active", len(g.sem)))
	}
	g.sem = make(chan token, n)
}

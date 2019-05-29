package nll

import (
	"context"
	"fmt"
	"sync"
)

// Reaper is a func type that reclaims the resources from a previously spawned
// (i.e. by a Spawner func) objecct or process.
type Reaper func(context.Context) error

// Spawner launches an object or process and returns a Reaper that will run
// later when the Scope instance executing the Spawner func exits.
type Spawner func(context.Context) (Reaper, error)

type state string

const (
	active state = "active"
	done   state = "done"
)

// Scoper is a func signature realized by both nll.NewScope and
// Scope.NewChildScope. It is useful to pass this abstraction around when the
// ability to create a new Scope instance is desirable but the code should not
// be coupled to a particular scope tree.
type Scoper func(...ScopeOpt) *Scope

// Scope provides for non-lexical lifetimes of objects and goroutines by holding
// onto a set of Reapers and child Scopes for execution at some dynamically
// determined point in the future (by calling Scope.Exit).
type Scope struct {
	mut      sync.Mutex
	state    state
	children []*Scope
	reapers  []Reaper
	errors   chan error
}

// ScopeOpt is a type for optional parameters to the Scope constructors.
type ScopeOpt func(*Scope)

// WithErrorChan yields a ScopeOpt that allows the creation of a new Scope that
// will use the `chan error` supplied here as its internal error channel
// (observable via Scope.Err).
func WithErrorChan(errs chan error) ScopeOpt {
	return func(s *Scope) {
		s.errors = errs
	}
}

// NewScope instantiates a Scope with the supplied options. The new Scope is
// immediately usable and remains so until Scope.Exit is invoked.
func NewScope(opts ...ScopeOpt) *Scope {
	s := &Scope{
		state:  active,
		errors: make(chan error),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// NewChildScope instantiates a new Scope instance that can be exited directly
// or which will be implicitly exited when one of its ancestor Scopes exits. If
// NewChildScope is called on a Scope instance that has aready exited, a Scope
// pointer will still be returned however that Scope will be useless as the
// child scope will inherit the state (in this case the exited state) of the
// creating parent.
func (s *Scope) NewChildScope(opts ...ScopeOpt) *Scope {
	s.mut.Lock()
	defer s.mut.Unlock()
	if s.state != active {
		return s
	}
	child := NewScope(opts...)
	s.children = append(s.children, child)
	return child
}

// Spawn invokes the supplied Spawner function and stores the returned Reaper
// for execution when this Scope exits. If the Spawner returns an error, that
// error is propagated as the retun value from this function. If this Scope has
// already exited then this function will return an error.
func (s *Scope) Spawn(ctx context.Context, sp Spawner) error {
	s.mut.Lock()
	defer s.mut.Unlock()
	if s.state != active {
		return fmt.Errorf("cannot spawn in scope with state %q", s.state)
	}
	r, err := sp(ctx)
	if err != nil {
		return err
	}
	s.reapers = append(s.reapers, r)
	return nil
}

type exitCfg struct {
	onError    func(err error)
	maybeClose func(chan error)
}

// ExitOpt is a type for optional parameters to the Scope.Exit function.
type ExitOpt func(*exitCfg)

// WithErrorHandler allows clients of Scope.Exit to supply a func that will be
// notified of errors that are returned by calls to Reaper instances. Note that
// this func does not allow for error propagation so the error must be handled.
func WithErrorHandler(eh func(err error)) ExitOpt {
	return func(cfg *exitCfg) {
		cfg.onError = eh
	}
}

// WithCloseErrorChan is an exit option that instructs the exiting Scope to
// close its error channel as part of the exit process. This option is
// propagated to any child Scope instances and thus will have the same effect on
// all descendents.
func WithCloseErrorChan() ExitOpt {
	return func(ec *exitCfg) {
		ec.maybeClose = func(errors chan error) { close(errors) }
	}
}

// Exit terminates this Scope instance by recursively exiting its descendent
// scopes in the reverse order of creation and then invoking all of it's managed
// Reaper functions again in the reverse of the order in which they were
// spawned. The *only* error emitted by this function is a if the supplied
// context.Context
func (s *Scope) Exit(ctx context.Context, opts ...ExitOpt) error {
	ec := exitCfg{
		onError:    func(err error) {},
		maybeClose: func(chan error) {},
	}
	for _, opt := range opts {
		opt(&ec)
	}
	return s.exit(ctx, &ec)
}

// Err observes this Scope's asynchronous error channel.
func (s *Scope) Err() chan error {
	return s.errors
}

func (s *Scope) exit(ctx context.Context, ec *exitCfg) error {
	s.mut.Lock()
	defer func() {
		s.reapers = make([]Reaper, 0)
		s.state = done
		ec.maybeClose(s.errors)
		s.mut.Unlock()
	}()
	if s.state != active {
		return nil
	}
	for i := len(s.children) - 1; i >= 0; i-- {
		err := s.children[i].exit(ctx, ec)
		if err != nil && err != ctx.Err() {
			ec.onError(err)
		}
		if ctxerr := ctx.Err(); ctxerr != nil {
			return ctxerr
		}
	}
	for i := len(s.reapers) - 1; i >= 0; i-- {
		err := s.reapers[i](ctx)
		if err != nil && err != ctx.Err() {
			ec.onError(err)
		}
		if ctxerr := ctx.Err(); ctxerr != nil {
			return ctxerr
		}
	}
	return nil
}

// FmtErrHandler takes a common func prototype -- viz. `func(string,
// ...interface{})` -- and adapts it to the error handler func type (optionally)
// used by Scope.Exit. An example use of this function would be to adapt the Go
// log.Printf func to an error handler.
func FmtErrHandler(f func(string, ...interface{}), fmt string) func(error) {
	return func(err error) { f(fmt, err) }
}

// MustSpawn is a helper function that passes the supplied Spawner to the
// Scope.Spawn function on the supplied Scope instance with the provided
// context. If an error is returned from Scope.Spawn then this function will
// panic.
func MustSpawn(ctx context.Context, sp Spawner, sc *Scope) {
	err := sc.Spawn(ctx, sp)
	if err != nil {
		panic(err)
	}
}

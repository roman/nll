package nll_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mmcshane/nll"
)

func nilReaper(context.Context) error { return nil }

func require(t *testing.T, expr bool, msg string, args ...interface{}) {
	t.Helper()
	if !expr {
		t.Fatalf(msg, args...)
	}
}

func TestSpawnReap(t *testing.T) {
	got := 0
	s := nll.NewScope()
	err := s.Spawn(context.TODO(), func(context.Context) (nll.Reaper, error) {
		got = 1
		return func(context.Context) error {
			got = 2
			return nil
		}, nil
	})

	require(t, err == nil, "unexpected error: %q", err)
	require(t, got == 1, "expected Scope.Spawn to have run Spawner")

	err = s.Exit(context.TODO())

	require(t, err == nil, "unexpected error from Scope.Exit: %q", err)
	require(t, got == 2, "expected Scope.Exit to have run Reaper")
}

func TestDoneState(t *testing.T) {
	s := nll.NewScope()
	s.Exit(context.TODO())

	err := s.Spawn(context.TODO(),
		func(context.Context) (nll.Reaper, error) { return nil, nil })
	require(t, err != nil,
		"expected error when spawning from a Scope in the 'done' state")

	child := s.NewChildScope()
	require(t, child != nil, "NewChildScope should not return nil")

	err = child.Spawn(context.TODO(),
		func(context.Context) (nll.Reaper, error) { return nil, nil })
	require(t, err != nil, "expected error when spawning from the "+
		"descendent of a Scope in the 'done' state")

	err = child.Exit(context.TODO())

	require(t, err == nil,
		"exiting a scope that is already done is not an error")
}

func TestExitErrorHandler(t *testing.T) {
	want := errors.New(t.Name())
	s := nll.NewScope()
	err := s.Spawn(context.TODO(), func(context.Context) (nll.Reaper, error) {
		return func(context.Context) error { return want }, nil
	})
	require(t, err == nil, "unexpected error: %q", err)

	var got error
	err = s.Exit(context.TODO(),
		nll.WithErrorHandler(func(err error) { got = err }))
	require(t, err == nil, "unexpected error: %q", err)
	require(t, want == got,
		"expected error handler invocation with %#v (got %#v)", want, got)
}

func TestAsyncErrors(t *testing.T) {
	want := errors.New(t.Name())
	out := make(chan error)
	s := nll.NewScope(nll.WithErrorChan(out))
	err := s.Spawn(context.TODO(), func(context.Context) (nll.Reaper, error) {
		go func() { s.Err() <- want }()
		return nilReaper, nil
	})
	require(t, err == nil, "unexpected error: %q", err)
	got := <-out
	require(t, want == got, "expected async error on output chan")
}

func TestSyncSpawnError(t *testing.T) {
	want := errors.New(t.Name())
	s := nll.NewScope()
	got := s.Spawn(context.TODO(), func(context.Context) (nll.Reaper, error) {
		return nil, want
	})
	require(t, want == got, "expected error. want: %q, got: %q", want, got)
}

const (
	uninitialized = 0
	spawned       = 1
	reaped        = 2
)

type testProcess int

func (tp *testProcess) Spawn(context.Context) (nll.Reaper, error) {
	*tp = spawned
	return tp.Reap, nil
}

func (tp *testProcess) Reap(context.Context) error {
	*tp = reaped
	return nil
}

func (tp *testProcess) Is(i int) bool { return int(*tp) == i }

func TestParentChild(t *testing.T) {
	//          root
	//          /  \
	//         a    d
	//        / \    \
	//       b   c    e

	root := nll.NewScope()
	a := root.NewChildScope()
	b := a.NewChildScope()
	c := a.NewChildScope()
	d := root.NewChildScope()
	e := d.NewChildScope()

	rootsvc := new(testProcess)
	asvc := new(testProcess)
	bsvc := new(testProcess)
	csvc := new(testProcess)
	dsvc := new(testProcess)
	esvc := new(testProcess)

	root.Spawn(context.TODO(), rootsvc.Spawn)
	a.Spawn(context.TODO(), asvc.Spawn)
	b.Spawn(context.TODO(), bsvc.Spawn)
	c.Spawn(context.TODO(), csvc.Spawn)
	d.Spawn(context.TODO(), dsvc.Spawn)
	e.Spawn(context.TODO(), esvc.Spawn)

	a.Exit(context.TODO())

	// scopes root, d, and e are still active so their service instances should
	// still be in the spawned state
	require(t, rootsvc.Is(spawned), "expected svc to be running")
	require(t, dsvc.Is(spawned), "expected svc to be running")
	require(t, esvc.Is(spawned), "expected svc to be running")

	// scope a was exited resulting in b and c also exiting. the services
	// associated with all three of those scopes should have been reaped.
	require(t, asvc.Is(reaped), "expected svc to have been reaped")
	require(t, bsvc.Is(reaped), "expected svc to have been reaped")
	require(t, csvc.Is(reaped), "expected svc to have been reaped")
}

func TestExitTimeout(t *testing.T) {
	s := nll.NewScope()
	exitctx, cncl := context.WithCancel(context.Background())
	s.Spawn(context.TODO(), func(context.Context) (nll.Reaper, error) {
		return func(ctx context.Context) error {
			cncl()
			return ctx.Err()
		}, nil
	})

	err := s.Exit(exitctx)
	require(t, err != nil && exitctx.Err() == err,
		"expected context cancellation error")
}

func TestFmtErrHandler(t *testing.T) {
	v := "🙃"
	format := "abc %s 123"
	want := fmt.Sprintf(format, v)
	var got string
	f := nll.FmtErrHandler(func(format string, v ...interface{}) {
		got = fmt.Sprintf(format, v...)
	}, format)
	f(errors.New(v))
	require(t, want == got, "expected %q, got %q", want, got)
}

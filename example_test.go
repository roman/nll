package nll_test

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmcshane/nll"
)

type Service struct {
	state    string
	newScope nll.Scoper
	stop     chan struct{}
	done     chan struct{}
}

func NewService(newScope nll.Scoper) *Service {
	return &Service{
		state:    "stopped",
		newScope: newScope,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (svc *Service) Stop(ctx context.Context) error {
	close(svc.stop)
	svc.state = "stopped"
	select {
	case <-svc.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (svc *Service) State() string {
	return svc.state
}

// HandleRequest simulates a long-running request to this service by sleeping
// for the supplied duration.
func (svc *Service) HandleRequest(ctx context.Context, d time.Duration) error {
	reqscope := svc.newScope()
	defer reqscope.Exit(context.TODO())

	// sapwn a watchdog agent but have it run more slowly than the request
	// processing so that it never fires (need predictable output for the test)
	err := spawnRequestWatchdog(ctx, reqscope, 10*d)
	if err != nil {
		return err
	}
	fmt.Printf("pretending to work by sleeping for %s\n", d)

	select {
	case <-time.After(d):
	case <-svc.stop:
	}
	// we don't have to clean up anything here as the deferred scope Exit
	// set up above will clean up the background request watchdog.
	return nil
}

func (svc *Service) ListenAndServe(ready chan<- struct{}) error {
	svc.state = "running"
	close(ready)
	<-svc.stop
	close(svc.done)
	return nil
}

func spawnRequestWatchdog(ctx context.Context, s *nll.Scope, d time.Duration) error {
	t := time.NewTicker(time.Duration(d))
	stop := make(chan struct{})
	done := make(chan struct{})
	return s.Spawn(ctx, func(context.Context) (nll.Reaper, error) {
		fmt.Println("launching request watchdog")
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					fmt.Println("request interrupted, cleaning up watchdog")
					return
				case <-t.C:
					fmt.Println("watchdog check")
				}
			}
		}()

		return func(ctx context.Context) error {
			close(stop)
			select {
			case <-done:
			case <-ctx.Done():
			}
			return nil
		}, nil
	})
}

func Example() {
	mainscope := nll.NewScope()
	svc := mustSpawnService(context.TODO(), mainscope)

	fmt.Printf("svc.State() == %q\n", svc.State())

	go func() {
		svc.HandleRequest(context.TODO(), 10*time.Second)
	}()

	mainwait(mainscope.Err())

	exitctx, _ := context.WithTimeout(context.Background(), time.Duration(3*time.Second))
	err := mainscope.Exit(exitctx)
	if err != nil {
		panic(err)
	}

	fmt.Printf("svc.State() == %q\n", svc.State())

	// Output:
	// svc.State() == "running"
	// launching request watchdog
	// pretending to work by sleeping for 10s
	// demo exits after 1 second
	// request interrupted, cleaning up watchdog
	// svc.State() == "stopped"
}

func mainwait(errs <-chan error) {
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errs:
		fmt.Printf("unhandled error: %q", err)
	case sig := <-sigchan:
		fmt.Printf("received signal (%v)", sig)
	case <-time.After(1 * time.Second):
		fmt.Println("demo exits after 1 second")
	}
}

func mustSpawnService(ctx context.Context, s *nll.Scope) *Service {
	svc := NewService(s.NewChildScope)
	ready := make(chan struct{}, 1)
	err := s.Spawn(ctx, func(context.Context) (nll.Reaper, error) {
		go func() {
			if err := svc.ListenAndServe(ready); err != nil {
				s.Err() <- err
			}
		}()
		return svc.Stop, nil
	})
	<-ready

	if err != nil {
		panic(err)
	}
	return svc
}

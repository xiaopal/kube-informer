package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func runInformer(ctx context.Context) {
	informer := NewInformer(kubeconfig, InformerOpts{
		Handler:     handleEvent,
		MaxRetries:  handlerMaxRetries,
		RateLimiter: handlerRateLimiter(),
	})
	for _, watch := range parsedWatches {
		err := informer.Watch(watch["apiVersion"], watch["kind"], namespace, selector, resyncDuration)
		if err != nil {
			logger.Fatalf("failed to watch %v: %v", watch, err)
		}
	}
	informer.Run(ctx)
	<-ctx.Done()
}

func safeContext() context.Context {
	interruptChan := make(chan os.Signal, 2)
	signal.Notify(interruptChan, syscall.SIGINT, syscall.SIGTERM)
	ctx, interrupt := context.WithCancel(context.Background())
	go func() {
		select {
		case sig := <-interruptChan:
			logger.Printf("signal %v", sig)
			interrupt()
		case <-ctx.Done():
		}
		signal.Stop(interruptChan)
	}()
	return ctx
}

func childrenReaper(ctx context.Context) {
	childChan := make(chan os.Signal, 10)
	signal.Notify(childChan, syscall.SIGCHLD)
	defer signal.Stop(childChan)
	for {
		select {
		case <-childChan:
			var wstatus syscall.WaitStatus
			for {
				if pid, _ := syscall.Wait4(-1, &wstatus, syscall.WNOHANG, nil); pid > 0 {
					continue
				}
				break
			}
		case <-ctx.Done():
			return
		}
	}
}

func main() {
	ctx := safeContext()

	if os.Getpid() == 1 {
		go childrenReaper(ctx)
	}

	leaderRun(ctx, runInformer)
}

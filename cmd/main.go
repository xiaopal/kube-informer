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
			logger.Printf("failed to watch %v: %v", watch, err)
			return
		}
	}
	informer.Run(ctx)
	<-ctx.Done()
}

func safeContext() (context.Context, func()) {
	interruptChan := make(chan os.Signal, 2)
	signal.Notify(interruptChan, syscall.SIGINT, syscall.SIGTERM)
	ctx, endCtx := context.WithCancel(context.Background())
	go func() {
		select {
		case sig := <-interruptChan:
			logger.Printf("signal %v", sig)
			endCtx()
		case <-ctx.Done():
		}
		signal.Stop(interruptChan)
	}()
	return ctx, endCtx
}

func main() {
	ctx, endCtx := safeContext()
	defer endCtx()

	if os.Getpid() == 1 {
		startChildrenReaper(ctx)
	}

	leaderRun(ctx, runInformer)
}

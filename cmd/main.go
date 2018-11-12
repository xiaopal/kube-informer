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

func main() {
	interruptChan := make(chan os.Signal, 2)
	signal.Notify(interruptChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(interruptChan)
	ctx, interrupt := context.WithCancel(context.Background())
	go func() {
		select {
		case sig := <-interruptChan:
			logger.Printf("signal %v", sig)
			interrupt()
		case <-ctx.Done():
		}
	}()
	leaderRun(ctx, runInformer)
}

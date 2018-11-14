package appctx

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

//Start func
func Start() (context.Context, func()) {
	logger := log.New(os.Stderr, "[appctx] ", log.Flags())
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

package main

import (
	"context"
	"os"

	"github.com/xiaopal/kube-informer/pkg/appctx"
	"github.com/xiaopal/kube-informer/pkg/subreaper"
)

func runInformer(ctx context.Context) {
	config, err := kubeClient.GetConfig()
	if err != nil {
		logger.Printf("failed to get config: %v", err)
		return
	}
	informer := NewInformer(config, InformerOpts{
		Handler:     handleEvent,
		MaxRetries:  handlerMaxRetries,
		RateLimiter: handlerRateLimiter(),
	})
	for _, watch := range parsedWatches {
		err := informer.Watch(watch["apiVersion"], watch["kind"], kubeClient.Namespace(), selector, resyncDuration)
		if err != nil {
			logger.Printf("failed to watch %v: %v", watch, err)
			return
		}
	}
	informer.Run(ctx)
	<-ctx.Done()
}

func main() {
	ctx, endCtx := appctx.Start()
	defer endCtx()

	if os.Getpid() == 1 {
		subreaper.Start(ctx)
	}
	leaderHelper.Run(ctx, runInformer)
}

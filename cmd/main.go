package main

import (
	"context"
	"os"

	"github.com/xiaopal/kube-informer/pkg/appctx"
	"github.com/xiaopal/kube-informer/pkg/subreaper"
	"k8s.io/client-go/tools/cache"
)

func runInformer(app appctx.Interface) {
	config, err := kubeClient.GetConfig()
	if err != nil {
		logger.Printf("failed to get config: %v", err)
		return
	}
	indexers := cache.Indexers{}
	if indexServer != "" {
		for name, template := range indexServerIndexes {
			if indexers[name], err = templateIndexer(name, template); err != nil {
				logger.Printf("failed to parse index %s: %v", name, err)
				return
			}
		}
	}
	informer := NewInformer(config, InformerOpts{
		Handler:     handleEvent,
		MaxRetries:  handlerMaxRetries,
		RateLimiter: handlerRateLimiter(),
		Indexers:    indexers,
	})
	for _, watch := range watches {
		err := informer.Watch(watch["apiVersion"], watch["kind"], kubeClient.Namespace(), labelSelector, fieldSelector, resyncDuration)
		if err != nil {
			logger.Printf("failed to watch %v: %v", watch, err)
			return
		}
	}
	if indexServer != "" {
		startIndexServer(app, indexServer, informer)
	}
	if err := informer.Run(app.Context()); err != nil {
		logger.Printf("informer exited: %v", err)
	}
}

func main() {
	app := appctx.Start()
	defer app.End()

	if os.Getpid() == 1 {
		subreaper.Start(app.Context())
	}
	leaderHelper.Run(app.Context(), func(ctx context.Context) {
		runInformer(app)
	})
}

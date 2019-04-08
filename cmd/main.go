package main

import (
	"context"
	"os"

	"github.com/xiaopal/kube-informer/pkg/appctx"
	"github.com/xiaopal/kube-informer/pkg/informer"
	"github.com/xiaopal/kube-informer/pkg/subreaper"
)

func runInformer(app appctx.Interface) {
	i := informer.NewInformer(kubeClient, informer.Opts{
		Logger:      logger,
		Handler:     handleEvent,
		MaxRetries:  handlerMaxRetries,
		RateLimiter: informer.DefaultRateLimiter(handlerRetriesBaseDelay, handlerRetriesMaxDelay, handlerLimitRate, handlerLimitBursts),
		Indexers:    httpServerIndexers,
	})
	for _, watch := range watches {
		err := i.Watch(watch["apiVersion"], watch["kind"], kubeClient.Namespace(), labelSelector, fieldSelector, resyncDuration)
		if err != nil {
			logger.Printf("failed to watch %v: %v", watch, err)
			return
		}
	}
	if httpServer != "" {
		locations := &informer.HTTPServerLocations{
			Health:       "/health",
			DefaultIndex: "/index",
			IndexPrefix:  "/index/",
		}
		if proxyAPIServer != "" {
			locations.APIProxyPrefix, locations.APIProxyAllow = "/api/", proxyAPIServer
		}
		i.EnableHTTPServerWithLocations(httpServer, *locations)
	}
	if err := i.Run(app.Context()); err != nil {
		logger.Printf("informer exited: %v", err)
	}
}

func main() {
	cmd := bindOptions(func() {
		app := appctx.Start()
		defer app.End()

		if os.Getpid() == 1 {
			subreaper.Start(app.Context())
		}
		leaderHelper.Run(app.Context(), func(ctx context.Context) {
			runInformer(app)
		})
	})

	if err := cmd.Execute(); err != nil {
		logger.Fatal(err)
	}
}

package informer

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
)

func (i *informer) Run(ctx context.Context) (err error) {
	logger, server := i.Logger, i.indexServer
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer i.queue.ShutDown()
	for _, watch := range i.watches {
		logger.Printf("watching %s", watch.name)
		go watch.watcher.Run(ctx.Done())
	}
	for _, watch := range i.watches {
		if !cache.WaitForCacheSync(ctx.Done(), watch.watcher.HasSynced) {
			return fmt.Errorf("wait for caches to sync")
		}
	}
	i.active = true
	go wait.Until(func() {
		for i.processNextItem(ctx) {
		}
	}, time.Second, ctx.Done())

	if server != nil {
		go func() {
			<-ctx.Done()
			logger.Printf("closing index server %s ...", server.Addr)
			if err := server.Close(); err != nil {
				logger.Printf("failed to close index server: %v", err)
			}
		}()
		logger.Printf("serving index on %s ...", server.Addr)
		if err = server.ListenAndServe(); err != nil {
			err = fmt.Errorf("index server exited: %v", err)
		}
		cancel()
	} else {
		<-ctx.Done()
	}
	i.active = false
	logger.Printf("stopped all watch")
	return
}

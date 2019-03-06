package main

import (
	"bytes"
	"context"
	"os"
	"sync"

	"github.com/xiaopal/kube-informer/pkg/appctx"
	"github.com/xiaopal/kube-informer/pkg/subreaper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/jsonpath"
)

func runInformer(ctx context.Context, wg *sync.WaitGroup) {
	config, err := kubeClient.GetConfig()
	if err != nil {
		logger.Printf("failed to get config: %v", err)
		return
	}
	indexers := cache.Indexers{}
	if indexServer != "" {
		for name, template := range indexServerIndexes {
			if indexers[name], err = jsonpathIndexer(name, template); err != nil {
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
	for _, watch := range parsedWatches {
		err := informer.Watch(watch["apiVersion"], watch["kind"], kubeClient.Namespace(), labelSelector, fieldSelector, resyncDuration)
		if err != nil {
			logger.Printf("failed to watch %v: %v", watch, err)
			return
		}
	}
	if indexServer != "" {
		startIndexServer(ctx, indexServer, informer, wg)
	}
	informer.Run(ctx)
	<-ctx.Done()
}

func jsonpathIndexer(name string, template string) (func(obj interface{}) ([]string, error), error) {
	j := jsonpath.New(name)
	if err := j.Parse(template); err != nil {
		return nil, err
	}
	return func(obj interface{}) ([]string, error) {
		buf := &bytes.Buffer{}
		if err := j.Execute(buf, obj.(*unstructured.Unstructured).UnstructuredContent()); err != nil {
			return []string{}, err
		}
		if buf.Len() > 0 {
			return []string{buf.String()}, nil
		}
		return []string{}, nil
	}, nil
}

func main() {
	app := appctx.Start()
	defer app.End()

	if os.Getpid() == 1 {
		subreaper.Start(app.Context())
	}
	leaderHelper.Run(app.Context(), func(ctx context.Context) {
		runInformer(app.Context(), app.WaitGroup())
	})
}

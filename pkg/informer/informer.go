package informer

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	dynamic "k8s.io/client-go/deprecated-dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/xiaopal/kube-informer/pkg/kubeclient"
)

//Opts type
type Opts struct {
	Logger      *log.Logger
	Handler     func(ctx context.Context, event EventType, obj *unstructured.Unstructured, numRetries int) error
	MaxRetries  interface{}
	RateLimiter workqueue.RateLimiter
	Indexers    cache.Indexers
}

//EventType type
type EventType string

const (
	//EventAdd constant
	EventAdd EventType = "add"
	//EventUpdate constant
	EventUpdate EventType = "update"
	//EventDelete constant
	EventDelete EventType = "delete"
)

type informer struct {
	Opts
	active         bool
	client   kubeclient.Client
	queue          workqueue.RateLimitingInterface
	deletedObjects objectMap
	watches        informerWatchList
	indexServer    *http.Server
}
type informerWatch struct {
	name     string
	informer *informer
	index    int
	watcher  cache.SharedIndexInformer
}

type informerWatchList []*informerWatch

type objectKey struct {
	watchIndex int
	key        string
}

type eventKey struct {
	objectKey
	event EventType
}

type objectMap map[objectKey]*unstructured.Unstructured

//DefaultRateLimiter func
func DefaultRateLimiter(baseDelay time.Duration, maxDelay time.Duration, limitRate float64, limitBursts int) workqueue.RateLimiter {
	return workqueue.NewMaxOfRateLimiter(
		workqueue.NewItemExponentialFailureRateLimiter(baseDelay, maxDelay),
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(limitRate), limitBursts)},
	)
}

//NewInformer func
func NewInformer(client kubeclient.Client, informerOpts Opts) Informer {
	opts := informerOpts
	if opts.Logger == nil {
		opts.Logger = log.New(os.Stderr, "[informer] ", log.Flags())
	}
	if opts.Indexers == nil {
		opts.Indexers = cache.Indexers{}
	}
	if opts.RateLimiter == nil {
		opts.RateLimiter = DefaultRateLimiter(5*time.Millisecond, 1000*time.Second, math.MaxFloat64, math.MaxInt32)
	}
	if _, ok := opts.MaxRetries.(int); !ok {
		opts.MaxRetries = 15
	}
	if opts.Handler == nil {
		opts.Handler = func(ctx context.Context, event EventType, obj *unstructured.Unstructured, numRetries int) error {
			opts.Logger.Printf("%s %s.%s: %s/%s", event, obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace(), obj.GetName())
			return nil
		}
	}
	return &informer{
		Opts:           opts,
		client:     client,
		queue:          workqueue.NewRateLimitingQueue(opts.RateLimiter),
		deletedObjects: objectMap{},
		watches:        informerWatchList{},
	}
}

//Informer interface
type Informer interface {
	Watch(apiVersion string, kind string, namespace string, labelSelector string, fieldSelector string, resync time.Duration) error
	GetIndexer(watchIndex int) (cache.Indexer, bool)
	Active() bool
	Run(ctx context.Context) error
	EnableIndexServer(serverAddr string) *http.ServeMux
}

func (i *informer) Watch(apiVersion string, kind string, namespace string, labelSelector string, fieldSelector string, resync time.Duration) error {
	client, resource, err := i.client.DynamicClient(apiVersion, kind)
	if err != nil {
		return err
	}
	if !resource.Namespaced {
		namespace = metav1.NamespaceAll
	}
	resourceClient, resourcePluralName := client.Resource(resource, namespace), resource.Name
	watch := &informerWatch{
		name:     fmt.Sprintf("%s/%s %s %s", namespace, resourcePluralName, labelSelector, fieldSelector),
		informer: i,
		index:    len(i.watches),
		watcher: cache.NewSharedIndexInformer(
			newListWatcherFromResourceClient(resourceClient, labelSelector, fieldSelector),
			&unstructured.Unstructured{},
			resync,
			i.Indexers,
		),
	}
	watch.watcher.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    watch.handleAdd,
		DeleteFunc: watch.handleDelete,
		UpdateFunc: watch.handleUpdate,
	})
	i.watches = append(i.watches, watch)
	return nil
}

func newListWatcherFromResourceClient(resourceClient dynamic.ResourceInterface, labelSelector string, fieldSelector string) *cache.ListWatch {
	listOptions := func(options metav1.ListOptions) metav1.ListOptions {
		if labelSelector != "" {
			options.LabelSelector = labelSelector
		}
		if fieldSelector != "" {
			options.FieldSelector = fieldSelector
		}
		return options
	}
	listFunc := func(options metav1.ListOptions) (runtime.Object, error) {
		return resourceClient.List(listOptions(options))
	}
	watchFunc := func(options metav1.ListOptions) (watch.Interface, error) {
		return resourceClient.Watch(listOptions(options))
	}
	return &cache.ListWatch{ListFunc: listFunc, WatchFunc: watchFunc}
}

func (i *informer) Active() bool {
	return i.active
}

func (i *informer) GetIndexer(watchIndex int) (cache.Indexer, bool) {
	if watchIndex < 0 || watchIndex >= len(i.watches) {
		return nil, false
	}
	return i.watches[watchIndex].watcher.GetIndexer(), true
}

func (w *informerWatch) handleAdd(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		panic(err)
	}
	w.informer.queue.Add(eventKey{objectKey{w.index, key}, EventAdd})
}

func (w *informerWatch) handleDelete(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		panic(err)
	}

	w.informer.deletedObjects[objectKey{w.index, key}] = obj.(*unstructured.Unstructured).DeepCopy()
	w.informer.queue.Add(eventKey{objectKey{w.index, key}, EventDelete})
}

func (w *informerWatch) handleUpdate(oldObj, newObj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(newObj)
	if err != nil {
		panic(err)
	}
	w.informer.queue.Add(eventKey{objectKey{w.index, key}, EventUpdate})
}

func (i *informer) processNextItem(ctx context.Context) bool {
	item, quit := i.queue.Get()
	if quit {
		return false
	}
	defer i.queue.Done(item)
	eventKey, numRetries := item.(eventKey), i.queue.NumRequeues(item)
	watcher := i.watches[eventKey.watchIndex].watcher
	obj, exists, err := watcher.GetIndexer().GetByKey(eventKey.key)
	if err == nil {
		if !exists {
			if _, ok := i.deletedObjects[eventKey.objectKey]; !ok {
				//logger.Printf("no last known state found for (%v)", eventKey)
				i.queue.Forget(item)
				return true
			}
			err = i.Handler(ctx, EventDelete, i.deletedObjects[eventKey.objectKey], numRetries)
		} else {
			err = i.Handler(ctx, eventKey.event, obj.(*unstructured.Unstructured).DeepCopy(), numRetries)
		}
	}
	if err != nil {
		maxRetries, _ := i.MaxRetries.(int)
		i.Logger.Printf("error processing (%v, retries %v/%v): %v", eventKey, numRetries, maxRetries, err)
		if maxRetries < 0 || numRetries < maxRetries {
			i.queue.AddRateLimited(item)
			return true
		}
	}
	if !exists {
		delete(i.deletedObjects, eventKey.objectKey)
	}
	i.queue.Forget(item)
	return true
}

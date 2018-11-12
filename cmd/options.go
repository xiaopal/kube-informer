package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"time"

	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/util/workqueue"
)

var (
	logger                         *log.Logger
	kubeconfigpath                 string
	masterURL                      string
	namespace                      string
	allNamespaces                  bool
	watches                        []string
	parsedWatches                  []map[string]string
	selector                       string
	resyncDuration                 time.Duration
	events                         []string
	handlerEvents                  map[EventType]bool
	handlerCommand                 []string
	handlerName                    string
	handlerPassStdin               bool
	handlerPassEnv                 bool
	handlerPassArgs                bool
	handlerMaxRetries              int
	handlerRetriesBaseDelay        time.Duration
	handlerRetriesMaxDelay         time.Duration
	leaderElectEnabled             bool
	leaderElectLeaseDuration       time.Duration
	leaderElectRenewDeadline       time.Duration
	leaderElectRetryPeriod         time.Duration
	leaderElectResourceLock        string
	leaderElectLockObjectName      string
	leaderElectLockObjectNamespace string
	kubeclient                     clientcmd.ClientConfig
	kubeconfig                     *rest.Config
	initialized                    bool
)

func parseWatch(watch string) map[string]string {
	opts := map[string]string{}
	for _, s := range strings.Split(watch, ",") {
		if opt := strings.SplitN(s, ":", 2); len(opt) == 2 {
			opts[strings.TrimSpace(opt[0])] = strings.TrimSpace(opt[1])
		}
	}
	return opts
}

func defaultNamespace() string {
	ns, _, err := kubeclient.Namespace()
	if err != nil {
		logger.Printf("fallback to default namespace: %v", err)
		return "default"
	}
	return ns
}

func initOptions(cmd *cobra.Command, args []string) (err error) {
	handlerCommand = args
	if len(handlerCommand) < 1 {
		return fmt.Errorf("handler args required")
	}
	if handlerName == "" {
		handlerName = filepath.Base(handlerCommand[0])
	}

	parsedWatches = []map[string]string{}
	for _, watch := range watches {
		parsedWatches = append(parsedWatches, parseWatch(watch))
	}
	if len(parsedWatches) < 1 {
		return fmt.Errorf("--watch required")
	}

	handlerEvents = map[EventType]bool{}
	for _, event := range events {
		handlerEvents[EventType(event)] = true
	}

	if allNamespaces {
		namespace = metav1.NamespaceAll
	}

	kubeclient = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigpath},
		&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: masterURL}})

	if !allNamespaces && namespace == "" {
		namespace = defaultNamespace()
	}

	leaderElectLockObjectName = strings.TrimSpace(leaderElectLockObjectName)
	if leaderElectEnabled = (leaderElectLockObjectName != ""); leaderElectEnabled {
		leaderElectResourceLock = resourcelock.EndpointsResourceLock
		if index := strings.Index(leaderElectLockObjectName, "/"); index > 0 {
			leaderElectResourceLock, leaderElectLockObjectName = leaderElectLockObjectName[:index], leaderElectLockObjectName[index+1:]
		}
		if leaderElectLockObjectNamespace == "" {
			leaderElectLockObjectNamespace = defaultNamespace()
		}
	}
	kubeconfig, err = kubeclient.ClientConfig()
	if err != nil {
		return fmt.Errorf("failed to get client config: %v", err)
	}
	return nil
}

func handlerRateLimiter() workqueue.RateLimiter {
	return workqueue.NewMaxOfRateLimiter(
		workqueue.NewItemExponentialFailureRateLimiter(5*time.Millisecond, 1000*time.Second),
		// 10 qps, 100 bucket size.  This is only for retry speed and its only the overall factor (not per item)
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)
}

func init() {
	logger = log.New(os.Stderr, "[kube-informer] ", log.Flags())
	cmd := &cobra.Command{
		Use:     fmt.Sprintf("%s [flags] handlerCommand args...", os.Args[0]),
		PreRunE: initOptions,
		Run: func(cmd *cobra.Command, args []string) {
			initialized = true
		},
	}

	if kubeconfigpath = os.Getenv("KUBECONFIG"); kubeconfigpath == "" {
		if home := os.Getenv("HOME"); home != "" {
			defaultKubeconfigpath := filepath.Join(home, ".kube", "config")
			if _, err := os.Stat(defaultKubeconfigpath); !os.IsNotExist(err) {
				kubeconfigpath = defaultKubeconfigpath
			}
		}
	}

	flags := cmd.Flags()
	flags.AddGoFlagSet(flag.CommandLine)
	flags.StringVar(&kubeconfigpath, "kubeconfig", kubeconfigpath, "path to the kubeconfig file")
	flags.StringVarP(&masterURL, "server", "s", "", "URL of the Kubernetes API server")
	flags.StringVarP(&namespace, "namespace", "n", "", "namespace")
	flags.BoolVar(&allNamespaces, "all-namespaces", false, "all namespaces")
	flags.StringArrayVarP(&watches, "watch", "w", []string{}, "watch resources, eg. `apiVersion:v1,kind:ConfigMap`")
	flags.StringVarP(&selector, "selector", "l", "", "selector (label query) to filter on")
	flags.DurationVar(&resyncDuration, "resync", 0, "resync period")
	flags.StringSliceVarP(&events, "events", "e", []string{string(EventAdd), string(EventUpdate), string(EventDelete)}, "handle events")
	flags.StringVar(&handlerName, "name", "", "handler name")
	flags.BoolVar(&handlerPassStdin, "pass-stdin", false, "pass obj json to handler stdin")
	flags.BoolVar(&handlerPassEnv, "pass-env", false, "pass obj json to handler env INFORMER_OBJECT")
	flags.BoolVar(&handlerPassArgs, "pass-args", false, "pass event and obj json to handler arg")
	flags.IntVar(&handlerMaxRetries, "max-retries", 15, "handler max retries, -1 for unlimited")
	flags.DurationVar(&handlerRetriesBaseDelay, "retries-base-delay", 5*time.Millisecond, "handler retries: base delay")
	flags.DurationVar(&handlerRetriesMaxDelay, "retries-max-delay", 1000*time.Second, "handler retries: max delay")
	flags.StringVar(&leaderElectLockObjectName, "leader-elect", os.Getenv("INFORMER_LEADER_ELECT"), "leader election: [endpoints|configmaps/]<object name>")
	flags.StringVar(&leaderElectLockObjectNamespace, "leader-elect-namespace", os.Getenv("INFORMER_LEADER_ELECT_NAMESPACE"), "leader election: object namespace")
	flags.DurationVar(&leaderElectLeaseDuration, "leader-elect-lease-duration", 15*time.Second, "leader election: lease duration")
	flags.DurationVar(&leaderElectRenewDeadline, "leader-elect-renew-deadline", 10*time.Second, "leader election: renew deadline")
	flags.DurationVar(&leaderElectRetryPeriod, "leader-elect-retry-period", 2*time.Second, "leader election: retry period")

	if err := cmd.Execute(); err != nil {
		logger.Fatal(err)
	}
	if !initialized {
		os.Exit(0)
	}
}

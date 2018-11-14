package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"time"

	"github.com/xiaopal/kube-informer/pkg/kubeclient"
	"github.com/xiaopal/kube-informer/pkg/leaderelect"

	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
)

var (
	logger                  *log.Logger
	watches                 []string
	parsedWatches           []map[string]string
	selector                string
	resyncDuration          time.Duration
	events                  []string
	handlerEvents           map[EventType]bool
	handlerCommand          []string
	handlerName             string
	handlerPassStdin        bool
	handlerPassEnv          bool
	handlerPassArgs         bool
	handlerMaxRetries       int
	handlerRetriesBaseDelay time.Duration
	handlerRetriesMaxDelay  time.Duration
	kubeClient              kubeclient.Client
	leaderHelper            leaderelect.Helper
	initialized             bool
)

func parseWatch(watch string) map[string]string {
	opts := map[string]string{}
	for _, s := range strings.Split(watch, ",") {
		if opt := strings.SplitN(s, "=", 2); len(opt) == 2 {
			opts[strings.TrimSpace(opt[0])] = strings.TrimSpace(opt[1])
		}
	}
	return opts
}

func initOptions(cmd *cobra.Command, args []string) (err error) {
	handlerCommand = args
	if len(handlerCommand) < 1 {
		return fmt.Errorf("handlerCommand required")
	}
	if handlerName == "" {
		handlerName = filepath.Base(handlerCommand[0])
	}

	parsedWatches = []map[string]string{}
	for _, line := range watches {
		for _, watch := range strings.Split(line, ":") {
			if strings.TrimSpace(watch) != "" {
				parsedWatches = append(parsedWatches, parseWatch(watch))
			}
		}
	}
	if len(parsedWatches) < 1 {
		return fmt.Errorf("--watch required")
	}

	handlerEvents = map[EventType]bool{}
	for _, event := range events {
		handlerEvents[EventType(event)] = true
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

func envToInt(key string, d int) int {
	if v := os.Getenv(key); v != "" {
		if ret, err := strconv.Atoi(v); err == nil {
			return ret
		}
	}
	return d
}

func envToDuration(key string, d time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if ret, err := time.ParseDuration(v); err == nil {
			return ret
		}
	}
	return d
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

	events = []string{string(EventAdd), string(EventUpdate), string(EventDelete)}
	if envEvents := os.Getenv("INFORMER_OPTS_EVENT"); envEvents != "" {
		events = strings.Fields(envEvents)
	}

	watches = []string{}
	if envWatch := os.Getenv("INFORMER_OPTS_WATCH"); envWatch != "" {
		watches = strings.Split(envWatch, ":")
	}

	flags := cmd.Flags()
	flags.AddGoFlagSet(flag.CommandLine)

	kubeClient = kubeclient.NewClient(&kubeclient.ClientOpts{})
	kubeClient.BindFlags(flags, "INFORMER_OPTS_")

	leaderHelper = leaderelect.NewHelper(&leaderelect.HelperOpts{
		DefaultNamespaceFunc: kubeClient.DefaultNamespace,
		GetConfigFunc:        kubeClient.GetConfig,
	})
	leaderHelper.BindFlags(flags, "INFORMER_OPTS_")

	flags.StringArrayVarP(&watches, "watch", "w", watches, "watch resources, eg. `apiVersion=v1,kind=ConfigMap`")
	flags.StringVarP(&selector, "selector", "l", os.Getenv("INFORMER_OPTS_SELECTOR"), "selector (label query) to filter on")
	flags.DurationVar(&resyncDuration, "resync", envToDuration("INFORMER_OPTS_RESYNC", 0), "resync period")
	flags.StringSliceVarP(&events, "event", "e", events, "handle events")
	flags.StringVar(&handlerName, "name", os.Getenv("INFORMER_OPTS_NAME"), "handler name")
	flags.BoolVar(&handlerPassStdin, "pass-stdin", os.Getenv("INFORMER_OPTS_PASS_STDIN") != "", "pass obj json to handler stdin")
	flags.BoolVar(&handlerPassEnv, "pass-env", os.Getenv("INFORMER_OPTS_PASS_ENV") != "", "pass obj json to handler env INFORMER_OBJECT")
	flags.BoolVar(&handlerPassArgs, "pass-args", os.Getenv("INFORMER_OPTS_PASS_ARGS") != "", "pass event and obj json to handler arg")
	flags.IntVar(&handlerMaxRetries, "max-retries", envToInt("INFORMER_OPTS_MAX_RETRIES", 15), "handler max retries, -1 for unlimited")
	flags.DurationVar(&handlerRetriesBaseDelay, "retries-base-delay", envToDuration("INFORMER_OPTS_RETRIES_BASE_DELAY", 5*time.Millisecond), "handler retries: base delay")
	flags.DurationVar(&handlerRetriesMaxDelay, "retries-max-delay", envToDuration("INFORMER_OPTS_RETRIES_MAX_DELAY", 1000*time.Second), "handler retries: max delay")

	if err := cmd.Execute(); err != nil {
		logger.Fatal(err)
	}
	if !initialized {
		os.Exit(0)
	}
}

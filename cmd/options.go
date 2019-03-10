package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
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
	logger                       *log.Logger
	watches                      = []map[string]string{}
	labelSelector, fieldSelector string
	resyncDuration               time.Duration
	handlerEvents                = map[EventType]bool{}
	handlerCommand               []string
	webhooks                     []*url.URL
	webhookTimeout               = 30 * time.Second
	handlerName                  string
	handlerPassStdin             bool
	handlerPassEnv               bool
	handlerPassArgs              bool
	handlerMaxRetries            int
	handlerRetriesBaseDelay      time.Duration
	handlerRetriesMaxDelay       time.Duration
	indexServer                  string
	indexServerIndexes           map[string]string
	kubeClient                   kubeclient.Client
	leaderHelper                 leaderelect.Helper
)

func parseWatch(watch string) map[string]string {
	opts := map[string]string{"apiVersion": "v1"}
	for _, s := range strings.Split(watch, ",") {
		if opt := strings.SplitN(s, "=", 2); len(opt) == 2 {
			opts[strings.TrimSpace(opt[0])] = strings.TrimSpace(opt[1])
		}
	}
	return opts
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
	//glog.CopyStandardLogTo("INFO")
	logger = log.New(os.Stderr, "[kube-informer] ", log.Flags())

	argWatches, argEvents, argWebhooks := []string{},
		[]string{string(EventAdd), string(EventUpdate), string(EventDelete)},
		[]string{}
	initOpts, checkOpts := func(cmd *cobra.Command) {
		flags := cmd.Flags()
		flags.AddGoFlagSet(flag.CommandLine)

		kubeClient = kubeclient.NewClient(&kubeclient.ClientOpts{})
		kubeClient.BindFlags(flags, "INFORMER_OPTS_")

		leaderHelper = leaderelect.NewHelper(&leaderelect.HelperOpts{
			DefaultNamespaceFunc: kubeClient.DefaultNamespace,
			GetConfigFunc:        kubeClient.GetConfig,
		})
		leaderHelper.BindFlags(flags, "INFORMER_OPTS_")

		if envWatchs := os.Getenv("INFORMER_OPTS_WATCH"); envWatchs != "" {
			argWatches = strings.Split(envWatchs, ":")
		}
		if envEvents := os.Getenv("INFORMER_OPTS_EVENT"); envEvents != "" {
			argEvents = strings.Split(envEvents, ",")
		}
		flags.StringArrayVarP(&argWatches, "watch", "w", argWatches, "watch resources, eg. `apiVersion=v1,kind=ConfigMap`")
		flags.StringVarP(&labelSelector, "selector", "l", os.Getenv("INFORMER_OPTS_SELECTOR"), "selector (label query) to filter on")
		flags.StringVar(&fieldSelector, "field-selector", os.Getenv("INFORMER_OPTS_FIELD_SELECTOR"), "selector (field query) to filter on")
		flags.DurationVar(&resyncDuration, "resync", envToDuration("INFORMER_OPTS_RESYNC", 0), "resync period")
		flags.StringSliceVarP(&argEvents, "event", "e", argEvents, "handle events")
		flags.StringVar(&handlerName, "name", os.Getenv("INFORMER_OPTS_NAME"), "handler name")
		flags.StringArrayVar(&argWebhooks, "webhook", argWebhooks, "define handler webhook")
		flags.DurationVar(&webhookTimeout, "webhook-timeout", webhookTimeout, "handler webhook timeout")
		flags.BoolVar(&handlerPassStdin, "pass-stdin", os.Getenv("INFORMER_OPTS_PASS_STDIN") != "", "pass obj json to handler stdin")
		flags.BoolVar(&handlerPassEnv, "pass-env", os.Getenv("INFORMER_OPTS_PASS_ENV") != "", "pass obj json to handler env INFORMER_OBJECT")
		flags.BoolVar(&handlerPassArgs, "pass-args", os.Getenv("INFORMER_OPTS_PASS_ARGS") != "", "pass event and obj json to handler arg")
		flags.IntVar(&handlerMaxRetries, "max-retries", envToInt("INFORMER_OPTS_MAX_RETRIES", 15), "handler max retries, -1 for unlimited")
		flags.DurationVar(&handlerRetriesBaseDelay, "retries-base-delay", envToDuration("INFORMER_OPTS_RETRIES_BASE_DELAY", 5*time.Millisecond), "handler retries: base delay")
		flags.DurationVar(&handlerRetriesMaxDelay, "retries-max-delay", envToDuration("INFORMER_OPTS_RETRIES_MAX_DELAY", 1000*time.Second), "handler retries: max delay")
		flags.StringVar(&indexServer, "index-server", indexServer, "index server bind addr, eg. `:8080`")
		flags.StringToStringVar(&indexServerIndexes, "index", indexServerIndexes, "index server indexs, define as jsonpath template, eg. `namespace='{.metadata.namespace}'`")
	}, func(cmd *cobra.Command, args []string) (err error) {
		if handlerName == "" {
			if handlerCommand = args; len(handlerCommand) > 0 {
				handlerName = filepath.Base(handlerCommand[0])
			} else {
				handlerName = "event"
			}
		}

		for _, line := range argWatches {
			for _, watch := range strings.Split(line, ":") {
				if strings.TrimSpace(watch) != "" {
					watches = append(watches, parseWatch(watch))
				}
			}
		}
		if len(watches) < 1 {
			return fmt.Errorf("--watch required")
		}

		for _, event := range argEvents {
			handlerEvents[EventType(event)] = true
		}

		for _, webhook := range argWebhooks {
			webhookURL, err := url.Parse(webhook)
			if err != nil {
				return fmt.Errorf("error to parse webhook %s: %v", webhook, err)
			}
			webhooks = append(webhooks, webhookURL)
		}

		return nil
	}

	initialized := false
	cmd := &cobra.Command{
		Use:     fmt.Sprintf("%s [flags] handlerCommand args...", os.Args[0]),
		PreRunE: checkOpts,
		Run: func(cmd *cobra.Command, args []string) {
			initialized = true
		},
	}
	initOpts(cmd)
	if err := cmd.Execute(); err != nil {
		logger.Fatal(err)
	}
	if !initialized {
		os.Exit(0)
	}
}

package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/spf13/cobra"

	"time"

	"github.com/xiaopal/kube-informer/pkg/informer"
	"github.com/xiaopal/kube-informer/pkg/kubeclient"
	"github.com/xiaopal/kube-informer/pkg/leaderelect"

	"text/template"

	"github.com/Masterminds/sprig"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

var (
	logger                       *log.Logger
	watches                      = []map[string]string{}
	labelSelector, fieldSelector string
	resyncDuration               time.Duration
	handlerEvents                = map[informer.EventType]bool{}
	handlerCommand               []string
	webhooks                     []*url.URL
	webhookTimeout               = 30 * time.Second
	webhookPayload               = true
	webhookParams                = map[string]func(obj *unstructured.Unstructured) (string, error){}
	handlerWhen                  func(obj *unstructured.Unstructured) (string, error)
	handlerName                  string
	handlerPassStdin             bool
	handlerPassEnv               bool
	handlerPassArgs              bool
	handlerMaxRetries            int
	handlerLimitRate             float64
	handlerLimitBursts           int
	handlerRetriesBaseDelay      time.Duration
	handlerRetriesMaxDelay       time.Duration
	httpServer, indexServer      string
	proxyAPIServer               string
	httpServerIndexers           = cache.Indexers{}
	templateDelims               = []string{"{{", "}}"}
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

func objectTemplate(name, tmplText string) (func(*unstructured.Unstructured) (string, error), error) {
	tmpl, err := template.New(name).Delims(templateDelims[0], templateDelims[1]).Funcs(sprig.TxtFuncMap()).Parse(tmplText)
	if err != nil {
		return nil, err
	}
	return func(obj *unstructured.Unstructured) (string, error) {
		buf := &bytes.Buffer{}
		if err := tmpl.Execute(buf, obj.UnstructuredContent()); err != nil {
			return "", err
		}
		if buf.String() == "<no value>" {
			return "", fmt.Errorf("<no value>")
		}
		return buf.String(), nil
	}, nil
}

func objectIndexer(name string, template string) (func(obj interface{}) ([]string, error), error) {
	keyFunc, err := objectTemplate(name, template)
	if err != nil {
		return nil, err
	}
	logger := log.New(os.Stderr, fmt.Sprintf("[index %s] ", name), log.Flags())
	return func(obj interface{}) ([]string, error) {
		if key, err := keyFunc(obj.(*unstructured.Unstructured)); err == nil && len(key) > 0 {
			return []string{key}, nil
		} else if err != nil && glog.V(3) {
			logger.Printf("error processing template: error=%v, obj=%v", err, obj)
		}
		return []string{}, nil
	}, nil
}

func bindOptions(mainProc func()) *cobra.Command {
	//glog.CopyStandardLogTo("INFO")
	logger = log.New(os.Stderr, "[kube-informer] ", log.Flags())

	argWatches, argEvents, argWebhooks, argWebhookParams, argIndexes, argWhen := []string{},
		[]string{string(informer.EventAdd), string(informer.EventUpdate), string(informer.EventDelete)},
		[]string{}, map[string]string{},
		map[string]string{},
		""
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
		flags.StringVar(&argWhen, "when", argWhen, "handler condition, define as go template(with sprig funcs)")
		flags.StringArrayVar(&argWebhooks, "webhook", argWebhooks, "define handler webhook")
		flags.DurationVar(&webhookTimeout, "webhook-timeout", webhookTimeout, "handler webhook timeout")
		flags.BoolVar(&webhookPayload, "webhook-payload", webhookPayload, "post object data to handler webhook")
		flags.StringToStringVar(&argWebhookParams, "webhook-param", argWebhookParams, "pass query param to handler webhook,define as go template(with sprig funcs), eg. `obj-name='{{.metadata.name}}'`")
		flags.BoolVar(&handlerPassStdin, "pass-stdin", os.Getenv("INFORMER_OPTS_PASS_STDIN") != "", "pass obj json to handler stdin")
		flags.BoolVar(&handlerPassEnv, "pass-env", os.Getenv("INFORMER_OPTS_PASS_ENV") != "", "pass obj json to handler env INFORMER_OBJECT")
		flags.BoolVar(&handlerPassArgs, "pass-args", os.Getenv("INFORMER_OPTS_PASS_ARGS") != "", "pass event and obj json to handler arg")
		flags.IntVar(&handlerMaxRetries, "max-retries", envToInt("INFORMER_OPTS_MAX_RETRIES", 15), "handler max retries, -1 for unlimited")
		flags.DurationVar(&handlerRetriesBaseDelay, "retries-base-delay", envToDuration("INFORMER_OPTS_RETRIES_BASE_DELAY", 5*time.Millisecond), "handler retries: base delay")
		flags.DurationVar(&handlerRetriesMaxDelay, "retries-max-delay", envToDuration("INFORMER_OPTS_RETRIES_MAX_DELAY", 1000*time.Second), "handler retries: max delay")
		flags.Float64Var(&handlerLimitRate, "limit-rate", 10, "handler limit: rate per second")
		flags.IntVar(&handlerLimitBursts, "limit-bursts", 100, "handler limit: bursts")
		flags.StringVar(&indexServer, "index-server", indexServer, "(DEPRECATED) http server bind addr, eg. `:8080` ")
		flags.StringVar(&httpServer, "http-server", httpServer, "http server bind addr, eg. `:8080` ")
		flags.MarkDeprecated("index-server", "use --http-server instead")
		flags.StringVar(&proxyAPIServer, "api-proxy", "", "proxy api server (on http server) with client ip whitelist, eg. 127.0.0.1, 10.0.0.0/8")
		flags.StringToStringVar(&argIndexes, "index", argIndexes, "http server indexs, define as go template(with sprig funcs), eg. `namespace='{{.metadata.namespace}}'`")
		flags.StringSliceVar(&templateDelims, "template-delims", templateDelims, "go template delims")
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
			handlerEvents[informer.EventType(event)] = true
		}

		if len(templateDelims) != 2 {
			return fmt.Errorf("invalid --template-delims")
		}

		if argWhen != "" {
			if handlerWhen, err = objectTemplate("when", argWhen); err != nil {
				return fmt.Errorf("error to parse handler condition %s: %v", argWhen, err)
			}
		}

		for _, webhook := range argWebhooks {
			webhookURL, err := url.Parse(webhook)
			if err != nil {
				return fmt.Errorf("error to parse webhook %s: %v", webhook, err)
			}
			webhooks = append(webhooks, webhookURL)
		}

		if httpServer == "" && indexServer != "" {
			httpServer = indexServer
		}
		if httpServer != "" {
			for name, template := range argIndexes {
				if httpServerIndexers[name], err = objectIndexer(name, template); err != nil {
					return fmt.Errorf("failed to parse index %s: %v", name, err)
				}
			}
		}

		for param, template := range argWebhookParams {
			if webhookParams[param], err = objectTemplate(param, template); err != nil {
				return fmt.Errorf("failed to parse webhook param %s: %v", param, err)
			}
		}

		return nil
	}

	cmd := &cobra.Command{
		Use:     fmt.Sprintf("%s [flags] handlerCommand args...", os.Args[0]),
		PreRunE: checkOpts,
		Run: func(cmd *cobra.Command, args []string) {
			mainProc()
		},
	}
	initOpts(cmd)
	return cmd
}

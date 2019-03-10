package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"

	"github.com/golang/glog"
	"github.com/xiaopal/kube-informer/pkg/subreaper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func handleEvent(ctx context.Context, event EventType, obj *unstructured.Unstructured, numRetries int) error {
	if !handlerEvents[event] {
		return nil
	}
	objJSON, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal obj: %v", err)
	}
	logger := log.New(os.Stderr, fmt.Sprintf("[%s] ", handlerName), log.Flags())
	if len(handlerCommand) > 0 {
		if err := executeHandlerCommand(ctx, event, obj, objJSON, numRetries, logger); err != nil {
			return err
		}
	} else {
		logger.Printf("%s %s.%s: %s/%s", event, obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace(), obj.GetName())
	}
	if len(webhooks) > 0 {
		if err := executeWebhooks(ctx, event, obj, objJSON, numRetries, logger); err != nil {
			return err
		}
	}
	return nil
}

func webhookRequest(webhookBase *url.URL, event EventType, obj *unstructured.Unstructured, objJSON []byte, numRetries int, logger *log.Logger) (*http.Request, error) {
	webhook, q := &url.URL{}, webhookBase.Query()
	q.Set("event", string(event))
	if numRetries > 0 {
		q.Set("retries", strconv.Itoa(numRetries))
	}
	for param, valFunc := range webhookParams {
		if val, err := valFunc(obj); err == nil {
			q.Set(param, val)
		} else if err != nil && glog.V(3) {
			logger.Printf("error processing param: error=%v, obj=%v", err, obj)
		}
	}
	*webhook = *webhookBase
	webhook.RawQuery = q.Encode()
	if !webhookPayload {
		return http.NewRequest("GET", webhook.String(), nil)
	}
	req, err := http.NewRequest("POST", webhook.String(), bytes.NewReader(objJSON))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, err
}

func executeWebhooks(ctx context.Context, event EventType, obj *unstructured.Unstructured, objJSON []byte, numRetries int, logger *log.Logger) error {
	for _, webhook := range webhooks {
		req, err := webhookRequest(webhook, event, obj, objJSON, numRetries, logger)
		if err != nil {
			return fmt.Errorf("failed to prepare webhook: %v", err)
		}
		reqCtx, endReq := context.WithTimeout(ctx, webhookTimeout)
		defer endReq()
		if res, err := http.DefaultClient.Do(req.WithContext(reqCtx)); err != nil {
			return fmt.Errorf("failed to process webhook %s: %v", req.URL.String(), err)
		} else if res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("failed to process webhook %s: HTTP %s", req.URL.String(), res.Status)
		} else if glog.V(2) {
			logger.Printf("triggerred webhook %s, %s %s.%s: %s/%s", req.URL.String(), event, obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace(), obj.GetName())
		}
	}
	return nil
}
func executeHandlerCommand(ctx context.Context, event EventType, obj *unstructured.Unstructured, objJSON []byte, numRetries int, logger *log.Logger) error {
	handler := exec.CommandContext(ctx, handlerCommand[0], handlerCommand[1:]...)
	if err := setupHandler(handler, event, obj, objJSON, numRetries, handlerMaxRetries, logger); err != nil {
		return fmt.Errorf("failed to setup handler: %v", err)
	}
	subreaper.Pause()
	defer subreaper.Resume()
	if err := handler.Run(); err != nil {
		return fmt.Errorf("failed to execute handler: %v", err)
	}
	return nil
}

func formatTimestamp(time *metav1.Time) string {
	if time == nil {
		return ""
	}
	ret, _ := time.MarshalQueryParameter()
	return ret
}

func setupHandler(handler *exec.Cmd, event EventType, obj *unstructured.Unstructured, objJSON []byte, numRetries int, maxRetries int, logger *log.Logger) error {
	creationTime := obj.GetCreationTimestamp()
	handler.Env = append(os.Environ(),
		fmt.Sprintf("INFORMER_EVENT=%s", event),
		fmt.Sprintf("INFORMER_RETRIES=%d", numRetries),
		fmt.Sprintf("INFORMER_MAX_RETRIES=%d", maxRetries),
		fmt.Sprintf("INFORMER_OBJECT_NAME=%s", obj.GetName()),
		fmt.Sprintf("INFORMER_OBJECT_NAMESPACE=%s", obj.GetNamespace()),
		fmt.Sprintf("INFORMER_OBJECT_API_VERSION=%s", obj.GetAPIVersion()),
		fmt.Sprintf("INFORMER_OBJECT_KIND=%s", obj.GetKind()),
		fmt.Sprintf("INFORMER_RESOURCE_VERSION=%s", obj.GetResourceVersion()),
		fmt.Sprintf("INFORMER_DELETION_TIMESTAMP=%s", formatTimestamp(obj.GetDeletionTimestamp())),
		fmt.Sprintf("INFORMER_CREATION_TIMESTAMP=%s", formatTimestamp(&creationTime)),
	)
	if handlerPassEnv {
		handler.Env = append(handler.Env, fmt.Sprintf("INFORMER_OBJECT=%s", string(objJSON)))
	}
	if handlerPassArgs {
		handler.Args = append(handler.Args, string(event), string(objJSON))
	}
	if handlerPassStdin {
		handler.Stdin = bytes.NewReader(objJSON)
	}
	if err := pipeStderr(handler, logger); err != nil {
		return fmt.Errorf("failed to pipe stderr: %v", err)
	}
	handler.Stdout = os.Stdout
	return nil
}

func pipeStderr(cmd *exec.Cmd, logger *log.Logger) error {
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	go func() {
		o := bufio.NewScanner(stderr)
		for o.Scan() {
			logger.Println(o.Text())
		}
	}()
	return nil
}

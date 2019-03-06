package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/xiaopal/kube-informer/pkg/subreaper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func handleEvent(ctx context.Context, event EventType, obj *unstructured.Unstructured, numRetries int) error {
	if !handlerEvents[event] {
		return nil
	}
	logger := log.New(os.Stderr, fmt.Sprintf("[%s] ", handlerName), log.Flags())
	if len(handlerCommand) == 0 {
		logger.Printf("%s %s.%s: name=%s, namespace=%s", event, obj.GetAPIVersion(), obj.GetKind(), obj.GetName(), obj.GetNamespace())
		return nil
	}
	handler := exec.CommandContext(ctx, handlerCommand[0], handlerCommand[1:]...)
	if err := setupHandler(handler, event, obj, numRetries, handlerMaxRetries, logger); err != nil {
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

func setupHandler(handler *exec.Cmd, event EventType, obj *unstructured.Unstructured, numRetries int, maxRetries int, logger *log.Logger) error {
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
	jsonObj, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal obj: %v", err)
	}
	if handlerPassEnv {
		handler.Env = append(handler.Env, fmt.Sprintf("INFORMER_OBJECT=%s", string(jsonObj)))
	}
	if handlerPassArgs {
		handler.Args = append(handler.Args, string(event), string(jsonObj))
	}
	if handlerPassStdin {
		handler.Stdin = bytes.NewReader(jsonObj)
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

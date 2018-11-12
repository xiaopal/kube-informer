package main

import (
	"context"
	"log"
	"os"

	"github.com/xiaopal/kube-informer/pkg/leaderelection"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes/scheme"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
)

func leaderRun(ctx context.Context, runFunc func(context.Context)) {
	if !leaderElectEnabled {
		runFunc(ctx)
		return
	}
	logger := log.New(os.Stderr, "[leader-election] ", log.Flags())
	lec, err := corev1.NewForConfig(kubeconfig)
	if err != nil {
		logger.Fatalf("failed to get CoreV1 Client: %v", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		logger.Fatalf("failed to get hostname: %v", err)
	}
	id := hostname + "_" + string(uuid.NewUUID())
	broadcaster := record.NewBroadcaster()
	lock, err := resourcelock.New(
		leaderElectResourceLock,
		leaderElectLockObjectNamespace,
		leaderElectLockObjectName,
		lec,
		resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: broadcaster.NewRecorder(scheme.Scheme, apiv1.EventSource{Component: "kube-informer"}),
		},
	)
	if err != nil {
		logger.Fatalf("failed to init resourcelock: %v", err)
	}

	le, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: leaderElectLeaseDuration,
		RenewDeadline: leaderElectRenewDeadline,
		RetryPeriod:   leaderElectRetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				logger.Printf("leader started: %s", id)
				runFunc(ctx)
			},
			OnStoppedLeading: func() {
				logger.Printf("leader lost")
			},
			OnNewLeader: func(identity string) {
				if identity == id {
					logger.Printf("entering leader: %s", identity)
					return
				}
				logger.Printf("following leader: %s", identity)
			},
		},
	})
	if err != nil {
		logger.Fatalf("failed to init leaderelector: %v", err)
	}
	le.Run(ctx)
}

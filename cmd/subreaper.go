package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var childrenReaperPauseChan chan bool

func pauseChildrenReaper(paused bool) {
	if childrenReaperPauseChan != nil {
		childrenReaperPauseChan <- paused
	}
}

func startChildrenReaper(ctx context.Context) {
	logger := log.New(os.Stderr, "[children-reaper] ", log.Flags())
	childrenReaperPauseChan = make(chan bool, 0)
	childChan := make(chan os.Signal, 10)
	leapChildren := func() {
		var wstatus syscall.WaitStatus
		for {
			if pid, _ := syscall.Wait4(-1, &wstatus, syscall.WNOHANG, nil); pid > 0 {
				logger.Printf("reap child pid %d, exitted %d", pid, wstatus.ExitStatus())
				continue
			}
			return
		}
	}
	go func() {
		signal.Notify(childChan, syscall.SIGCHLD)
		defer signal.Stop(childChan)
		paused := false
		for {
			select {
			case paused = <-childrenReaperPauseChan:
				if !paused {
					leapChildren()
				}
			case <-childChan:
				if !paused {
					leapChildren()
				}
			case <-ctx.Done():
				close(childChan)
				close(childrenReaperPauseChan)
				return
			}
		}
	}()
}

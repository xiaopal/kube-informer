package subreaper

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

//Pause func
func Pause() {
	pause(true)
}

//Resume func
func Resume() {
	pause(true)
}

var pauseChan chan bool

func pause(paused bool) {
	if pauseChan != nil {
		pauseChan <- paused
	}
}

//Start func
func Start(ctx context.Context) {
	logger := log.New(os.Stderr, "[children-reaper] ", log.Flags())
	pauseChan = make(chan bool, 0)
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
		defer func() {
			signal.Stop(childChan)
			close(childChan)
			close(pauseChan)
		}()
		paused := false
		for {
			select {
			case paused = <-pauseChan:
				if !paused {
					leapChildren()
				}
			case <-childChan:
				if !paused {
					leapChildren()
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

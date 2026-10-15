package aweprometheu

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

var (
	ctxRoot     context.Context
	ctxCancelFn context.CancelFunc
	IsStop      atomic.Bool
)

func InitCTX() context.Context {
	ctxBG := context.Background()
	ctxCancel, fnCancel := context.WithCancel(ctxBG)
	ctxRoot = ctxCancel
	ctxCancelFn = fnCancel

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	{
		go func() {
			s := <-sigChan
			lg().Info("catch_signal", "signal", s.String())
			IsStop.Store(true)
			if ctxRoot != nil {
				ctxCancelFn()
			}
		}()
	}
	return ctxRoot
}

package aweprometheu

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var (
	ctxRoot     context.Context
	ctxCancelFn context.CancelFunc
	IsStop      bool
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
			log.Println("Catch signal", s)
			IsStop = true
			if ctxRoot != nil {
				ctxCancelFn()
			}
		}()
	}
	return ctxRoot
}

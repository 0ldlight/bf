// Command binflow 运行显式健康或已认证下载服务。
// Command binflow runs the explicit health or authenticated download service.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/0ldlight/bf/internal/process"
)

// run 返回状态码，确保信号与服务资源的 defer 在 os.Exit 前执行。
// run returns an exit code so signal and service defers execute before os.Exit.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := process.LoadConfig(os.Getenv)
	if err != nil {
		log.Print("binflow: invalid configuration")
		return 1
	}
	err = process.Run(ctx, cfg, func(addr string) { log.Printf("binflow: listening on %s", addr) })
	if err != nil {
		log.Printf("binflow: service stopped: %v", err)
		return 1
	}
	log.Print("binflow: stopped")
	return 0
}
func main() { os.Exit(run()) }

// lifecycle.go 实现进程私有的 HTTP 生命周期：独立请求上下文、入场计数、
// 封场、有界排空、强制回收与延迟收割器。本文件属于运行时编写者所有。
// lifecycle.go implements the process-private HTTP lifetime: an independent
// request base context, admission counting, sealing, bounded drain, forced
// close and the deferred reaper. Owned by the runtime writer.
package process

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	// serveReadHeaderTimeout 是固定的 Slowloris 头部读取上限；沿用既有
	// 健康入口 5s 政策，不设置整体 WriteTimeout（下载流不允许被全局截断）。
	// serveReadHeaderTimeout is the fixed Slowloris header bound; it keeps
	// the existing 5s health policy. No whole-response WriteTimeout exists
	// because download streams must not be globally truncated.
	serveReadHeaderTimeout = 5 * time.Second
	// serveSealedBody 是封场后新请求的固定 503 正文；HEAD 不写正文。
	// serveSealedBody is the fixed 503 body for requests admitted after
	// sealing; HEAD never writes it.
	serveSealedBody = "service unavailable\n"
)

// 固定日志类别：只输出类别文本，绝不携带原始错误内容、DSN、路径或配置。
// Fixed log categories: category text only, never raw error contents, DSNs,
// paths or configuration.
const (
	logCategoryHTTPServer   = "process: http server error"
	logCategoryDrainTimeout = "process: shutdown drain timed out"
	logCategoryResourceLate = "process: deferred resource close failed"
)

// serveHTTPErrorLogWriter 丢弃 http.Server ErrorLog 的消息内容，仅输出固定
// 类别行；服务端 panic/内部错误文本从不进入日志。
// serveHTTPErrorLogWriter discards the http.Server ErrorLog message contents
// and emits only the fixed category line; server-side panic or internal error
// text never reaches the log.
type serveHTTPErrorLogWriter struct{}

func (serveHTTPErrorLogWriter) Write(p []byte) (int, error) {
	os.Stderr.WriteString(logCategoryHTTPServer + "\n")
	return len(p), nil
}

// admissionTracker 是私有入场计数器：入场递增与封场共享同一互斥锁；封场后
// 新请求得到固定 503 且不接触任何依赖；延迟递减覆盖 panic 与
// ErrAbortHandler；drained 通道在“已封场且计数归零”时恰一次关闭。
// admissionTracker is the private admission counter: entry increments and
// sealing share one mutex; post-seal requests get the fixed 503 without
// touching any dependency; the deferred decrement covers panics and
// ErrAbortHandler; the drained channel closes exactly once when sealed and
// the count reaches zero.
type admissionTracker struct {
	mu        sync.Mutex
	count     int
	sealed    bool
	drained   bool
	drainedCh chan struct{}
}

func newAdmissionTracker() *admissionTracker {
	return &admissionTracker{drainedCh: make(chan struct{})}
}

// enter 在未封场时登记一次入场；已封场返回 false，调用方必须直接回答固定
// 503，绝不调用下游处理器。
// enter registers one admission unless sealed; a false return obliges the
// caller to answer the fixed 503 directly and never invoke the downstream
// handler.
func (t *admissionTracker) enter() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sealed {
		return false
	}
	t.count++
	return true
}

// leave 递减一次入场；封场后计数归零时恰一次关闭 drained 通道。defer 调用
// 保证 panic 路径同样释放计数。
// leave decrements one admission and closes the drained channel exactly once
// when sealed and the count reaches zero. Deferred invocation guarantees the
// panic path releases the count too.
func (t *admissionTracker) leave() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.count > 0 {
		t.count--
	}
	if t.sealed && t.count == 0 && !t.drained {
		t.drained = true
		close(t.drainedCh)
	}
}

// seal 停止一切新入场；若已无在途请求则立即完成 drained。
// seal stops all future admissions; with no in-flight requests it completes
// drained immediately.
func (t *admissionTracker) seal() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sealed {
		return
	}
	t.sealed = true
	if t.count == 0 && !t.drained {
		t.drained = true
		close(t.drainedCh)
	}
}

// isDrained 是 drained 状态的即时快照。
// isDrained is an instantaneous snapshot of the drained state.
func (t *admissionTracker) isDrained() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.drained
}

// drainedChannel 返回恰一次关闭的排空信号通道。
// drainedChannel returns the drain signal channel that closes exactly once.
func (t *admissionTracker) drainedChannel() <-chan struct{} {
	return t.drainedCh
}

// handler 包装下游处理器：入场登记 + 延迟递减；封场后的请求得到固定 503
// （HEAD 无正文）且不接触依赖。本包装是“处理器实际返回”的唯一权威计数。
// handler wraps the downstream handler: admission on entry plus a deferred
// decrement; post-seal requests get the fixed 503 (no HEAD body) without
// touching dependencies. This wrapper is the single authority counting actual
// handler returns.
func (t *admissionTracker) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !t.enter() {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(serveSealedBody)))
			w.WriteHeader(http.StatusServiceUnavailable)
			if r.Method != http.MethodHead {
				w.Write([]byte(serveSealedBody))
			}
			return
		}
		defer t.leave()
		next.ServeHTTP(w, r)
	})
}

// serve 是进程私有的服务生命周期缝：调用方转入已绑定的监听器与已构造的
// 处理器，本函数独占监听、就绪回调、封场、排空与资源释放决策。请求基础
// 上下文派生自 background：取消 Run 只触发优雅排空，绝不取消已入场下载；
// 仅在强制路径取消请求上下文。closeOwned 由调用方提供（健康模式为无操作），
// 在“全部已入场处理器实际返回”之前绝不会被调用；排空超时后资源保持到
// 处理器实际返回，由唯一延迟收割器关闭。shutdown 必须为正。
// serve is the process-private lifetime seam: the caller transfers an already
// bound listener and a constructed handler, and this function alone owns
// serving, readiness, sealing, draining and resource release. The request
// base context derives from background: canceling Run triggers only a
// graceful drain and never cancels admitted downloads; the request context is
// canceled solely on the forced path. closeOwned comes from the caller (a
// no-op in health mode) and is never invoked before every admitted handler
// has actually returned; after a drain timeout resources are retained until
// actual handler return and closed by the single deferred reaper. shutdown
// must be positive.
func serve(ctx context.Context, ln net.Listener, handler http.Handler, shutdown time.Duration, closeOwned func() error, onReady func(string)) (err error) {
	if ctx == nil || ln == nil || handler == nil || closeOwned == nil || shutdown <= 0 {
		return ErrStartup
	}
	tracker := newAdmissionTracker()
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv := &http.Server{
		Handler:           tracker.handler(handler),
		ReadHeaderTimeout: serveReadHeaderTimeout,
		ErrorLog:          log.New(serveHTTPErrorLogWriter{}, "", 0),
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	// 就绪线性化点 = 最终 ctx 检查：监听器已实际绑定、依赖已由调用方构造
	// 校验；onReady 是可信回调（非阻塞、不 panic），仅接收绑定地址，无任何
	// 绝对调度/OS/回调时序保证。已取消的 ctx 直接进入停机，不报告就绪。
	// Readiness linearization = the final ctx check: the listener is actually
	// bound and dependencies were validated by the caller; onReady is a
	// trusted callback (nonblocking, nonpanicking) receiving only the bound
	// address, with no absolute scheduler/OS/callback timing guarantee. An
	// already canceled ctx enters shutdown directly without reporting ready.
	ready := ctx.Err() == nil
	if ready && onReady != nil {
		onReady(ln.Addr().String())
	}

	// closeOwnedSynchronized 在同步路径恰一次调用 closeOwned；任何关闭错误
	// 只记录固定类别并汇入公共 ErrResources 哨兵，绝不携带原始关闭错误。
	// closeOwnedSynchronized invokes closeOwned exactly once on the
	// synchronous path; any close error is logged by fixed category only and
	// joined as the public ErrResources sentinel, never as the raw cause.
	var closeOnce sync.Once
	closeOwnedSynchronized := func() {
		closeOnce.Do(func() {
			if cerr := closeOwned(); cerr != nil {
				log.Print(logCategoryResourceLate)
				err = errors.Join(err, ErrResources)
			}
		})
	}
	// launchReaper 启动唯一的显式延迟收割器：等待 drained（全部处理器实际
	// 返回）后恰一次关闭资源；关闭失败仅记录固定类别。本函数立即返回，
	// 不做无限期处理器汇合。
	// launchReaper starts the single explicit deferred reaper: it waits for
	// drained (every handler actually returned) and then closes resources
	// exactly once; close failures are logged by fixed category only. The
	// function returns immediately without an unbounded handler join.
	launchReaper := func() {
		go func() {
			<-tracker.drainedChannel()
			closeOnce.Do(func() {
				if cerr := closeOwned(); cerr != nil {
					log.Print(logCategoryResourceLate)
				}
			})
		}()
	}

	// forcedClose 是强制路径：Server.Close 并取消请求基础上下文。Close 不是
	// 处理器已返回的证明，仅有的权威是入场计数器。
	// forcedClose is the forced path: Server.Close plus canceling the request
	// base context. Close is NOT proof handlers returned; the admission
	// counter is the only authority.
	forcedClose := func() {
		cancelBase()
		_ = srv.Close()
	}

	// 两类停止共享同一个排空预算，不能在 Shutdown 后重新计时。
	// Both stop paths share one drain budget without restarting its timer.
	stoppedByContext := false
	select {
	case <-serveErr:
		err = ErrServe
	case <-ctx.Done():
		stoppedByContext = true
	}
	tracker.seal()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), shutdown)
	shutdownErr := srv.Shutdown(drainCtx)
	if !tracker.isDrained() {
		select {
		case <-tracker.drainedChannel():
		case <-drainCtx.Done():
			shutdownErr = drainCtx.Err()
		}
	}
	drainCancel()
	if stoppedByContext {
		<-serveErr
	}
	if !tracker.isDrained() || errors.Is(shutdownErr, context.DeadlineExceeded) || errors.Is(shutdownErr, context.Canceled) {
		log.Print(logCategoryDrainTimeout)
		forcedClose()
		err = errors.Join(err, ErrDrainTimeout)
	} else if shutdownErr != nil {
		err = errors.Join(err, ErrServe)
	}

	// 最终化：始终确保监听器关闭/服务器停止（幂等）；已排空则同步恰一次
	// 关闭受管资源；未排空（超时或异常路径）启动唯一收割器后立即返回，
	// 不声称排空成功。收割器保持资源直到处理器实际返回。
	// Finalize: always ensure the listener is closed and the server stopped
	// (idempotent); when drained close owned resources exactly once
	// synchronously; otherwise (timeout or abnormal path) launch the single
	// reaper and return immediately, never claiming a successful drain. The
	// reaper retains resources until actual handler return.
	if tracker.isDrained() {
		closeOwnedSynchronized()
	} else {
		launchReaper()
	}
	_ = srv.Close()
	return err
}

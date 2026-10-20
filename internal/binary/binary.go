// Package binary 编排“先写内容 provider、后登记元数据”的二进制写入顺序。
// Package binary orchestrates the provider-before-registration write order.
package binary

import (
	"context"
	"errors"
	"io"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
)

// 阶段文案是固定常量：Error() 永远只输出阶段归因，底层错误对象不经过任何
// 格式化，其文本（可能含请求体、摘要或凭据）不会自动出现在 Error() 里。
// Stage texts are fixed constants: Error() always prints only the stage
// attribution. The underlying error object is never formatted in, so its
// text (which may carry bodies, digests or credentials) cannot leak into
// Error() automatically. Unwrap still exposes the untouched original cause
// for trusted callers; HTTP responses and logging must not blindly
// serialize it.
const (
	stageCancelledBeforePut      = "binary: context cancelled before provider put"
	stageProviderPut             = "binary: provider put failed"
	stageCancelledBeforeRegister = "binary: context cancelled before registration"
	stageRegistryRegister        = "binary: registry register failed"
)

// stageError 携带固定阶段文案与原始原因；不修改、不包装原始错误对象本身。
// stageError pairs a fixed stage text with the original cause; the original
// error object is neither redacted nor mutated.
type stageError struct {
	stage string
	cause error
}

func (e *stageError) Error() string { return e.stage }
func (e *stageError) Unwrap() error { return e.cause }

// Provider 是内容持久化依赖：以 SHA-1 为身份消费调用方读取器并返回内容元数据。
// filestore.Store 直接满足该接口；本切片没有第二个实现。
// Provider persists content: it consumes the caller-owned reader once and
// returns computed content metadata. filestore.Store satisfies it directly;
// this slice ships no second implementation.
type Provider interface {
	Put(ctx context.Context, source io.Reader, expected filestore.Expected) (content.Metadata, error)
}

// Registry 登记内容元数据并返回规范结果。登记层独立拥有元数据提交策略：
// 已有数据库记录可作为规范结果，允许与刚流式计算的元数据不同。本切片没有
// 数据库适配器；SQL 事务接入是后续工作。
// Register commits metadata under its own policy and returns the canonical
// result: an existing database row may legitimately differ from freshly
// streamed metadata. There is no database adapter in this slice; wiring the
// SQL transaction remains future work.
type Registry interface {
	Register(ctx context.Context, meta content.Metadata) (content.Metadata, error)
}

// Service 是无内部可变调用状态的最小编排器；依赖支持时允许并发 Add。
// Service is a minimal stateless orchestrator; concurrent Adds are fine
// whenever the dependencies support them.
type Service struct {
	provider Provider
	registry Registry
}

// New 拒绝空依赖，返回有界配置错误。调用方保证传入有效实现（非接口类型化
// nil）并拥有其生命周期；本切片不做基于反射的类型化 nil 检测。
// New rejects nil dependencies with a bounded configuration error. The
// caller supplies valid implementations (not typed interface nils) and owns
// their lifetime; no reflection-based typed-nil detection is performed.
func New(provider Provider, registry Registry) (*Service, error) {
	if provider == nil {
		return nil, errors.New("binary: nil provider dependency")
	}
	if registry == nil {
		return nil, errors.New("binary: nil registry dependency")
	}
	return &Service{provider: provider, registry: registry}, nil
}

// Add 先调用 provider.Put（恰好一次），成功后在登记前复查 context，再调用
// registry.Register（恰好一次）并返回其规范结果。失败一律返回零元数据并以
// errors.Is 保留原始原因；错误文本只含阶段归因，不含读取器内容、摘要或凭据。
//
// 登记失败不删除/补偿 provider 已发布的内容，也不自动重试；调用方可换新
// 读取器重试并可能复用既有对象。顺序与目标 addBinary(BinaryStream) 的静态
// 证据一致：provider 在前，记录登记事务在后，正常失败处理不回滚内容。
// Add calls provider.Put exactly once, rechecks the context before
// registration, then calls registry.Register exactly once and returns its
// canonical result. Failures return zero metadata with the original cause
// preserved via errors.Is; error text carries only stage attribution, never
// reader bytes, digests or credentials.
//
// A registry failure never deletes or compensates published content and is
// never retried automatically; a caller may retry with a fresh reader and
// may hit object reuse. The ordering matches the static evidence of the
// target addBinary(BinaryStream): provider first, record-registration
// transaction second, and the normal failure handler keeps the content.
func (s *Service) Add(ctx context.Context, reader io.Reader, expected filestore.Expected) (content.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return content.Metadata{}, &stageError{stage: stageCancelledBeforePut, cause: err}
	}
	stored, err := s.provider.Put(ctx, reader, expected)
	if err != nil {
		return content.Metadata{}, &stageError{stage: stageProviderPut, cause: err}
	}
	if err := ctx.Err(); err != nil {
		return content.Metadata{}, &stageError{stage: stageCancelledBeforeRegister, cause: err}
	}
	registered, err := s.registry.Register(ctx, stored)
	if err != nil {
		return content.Metadata{}, &stageError{stage: stageRegistryRegister, cause: err}
	}
	return registered, nil
}

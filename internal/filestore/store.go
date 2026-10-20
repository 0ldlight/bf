// Package filestore 提供本地文件内容存储的校验与布局基础。
// Package filestore provides validation and layout primitives for the
// local file content store.
package filestore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/0ldlight/bf/internal/content"
)

// stageDirName 是 root 下的私有暂存目录；内容由服务身份独占管理。
// stageDirName is the private staging directory under the root; the
// service identity owns its contents exclusively.
const stageDirName = ".staging"

// Store 是绑定单一 root 目录的本地内容存储；Close 前调用方需先静默在途操作。
// Store is a local content store bound to one root directory; the caller
// quiesces in-flight operations before Close.
type Store struct {
	root *os.Root
	// stageOpen/renameFile/removeFile 是逐 store 的可替换原语，仅用于测试
	// 注入；默认是真实的 os.Root 操作。相对路径均以 root 为基准。
	// Per-store overridable primitives for test injection only; defaults
	// perform real os.Root operations. Paths are root-relative.
	stageOpen  func(name string) (io.WriteCloser, error)
	renameFile func(oldName, newName string) error
	removeFile func(name string) error
}

// New 校验并绑定 root：先词法清掉尾部 "/"、"/."、"//" 别名再 Lstat，使 root
// 符号链接策略无法被后缀绕过；拒绝空路径、符号链接与非目录；缺失时以 0700
// 创建（已存在的调用方目录不改权限），打开持久 os.Root 并确保 0700 私有
// staging。权限前提是显式本地安全策略，不是目标实现权限对齐：root 不得被
// 组/其他写（0022），.staging 不得有任何组/其他访问（0077）。
// New validates and binds a root: trailing "/", "/." and "//" aliases are
// lexically cleaned BEFORE Lstat so the root symlink policy cannot be
// bypassed by suffixes; empty paths, symlinks and non-directories are
// rejected; a missing root is created 0700 (existing caller directories are
// never chmodded), a persistent os.Root is opened and a private 0700
// staging directory ensured. The permission preconditions are an explicit
// local safety policy, not target permissions parity: the root must not be
// group/other writable (0022) and .staging must carry no group/other
// access (0077).
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("filestore: empty root")
	}
	if !platformSupported() {
		return nil, errors.New("filestore: platform unsupported: safe open semantics require Unix")
	}
	root = filepath.Clean(root)
	fi, err := os.Lstat(root)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("filestore: root is a symlink: %w", ErrUnsafePath)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("filestore: root not a directory: %w", ErrUnsafePath)
		}
		if fi.Mode().Perm()&0o022 != 0 {
			return nil, fmt.Errorf("filestore: root writable by group/others: %w", ErrUnsafePath)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, fmt.Errorf("filestore: create root: %w", err)
		}
		if fi, err := os.Lstat(root); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("filestore: root not a directory after create: %w", ErrUnsafePath)
		}
	default:
		return nil, fmt.Errorf("filestore: inspect root: %w", err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("filestore: open root: %w", err)
	}
	s := &Store{
		root: r,
		stageOpen: func(name string) (io.WriteCloser, error) {
			return r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		},
		renameFile: r.Rename,
		removeFile: r.Remove,
	}
	if err := s.ensureDir(stageDirName, "staging", 0o077); err != nil {
		r.Close()
		return nil, err
	}
	return s, nil
}

// Close 释放底层 os.Root；在途 Put/Open 的行为由调用方静默策略保证。
// Close releases the underlying os.Root; in-flight Put/Open behavior is
// governed by the caller's quiescing policy.
func (s *Store) Close() error {
	if err := s.root.Close(); err != nil {
		return fmt.Errorf("filestore: close root: %w", err)
	}
	return nil
}

// Put 流式消费调用方持有的 source（绝不关闭），在私有 staging 落盘并校验
// 期望摘要/长度后，以同文件系统重命名原子发布；已存在的普通对象直接复用。
// 任何错误返回零 Metadata，并总是清理本次 staging；清理错误与原始错误一并
// 返回，绝不删除最终对象或无关 staging。
// Put streams the caller-owned source (never closed) into private staging,
// validates expected digests/size, then publishes atomically via a
// same-filesystem rename; existing regular objects are reused as-is. Any
// error returns a zero Metadata and always removes this attempt's staging;
// cleanup errors are reported alongside the original cause, and final
// objects or unrelated staging are never removed.
func (s *Store) Put(ctx context.Context, source io.Reader, expected Expected) (content.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return content.Metadata{}, fmt.Errorf("filestore: cancelled: %w", err)
	}
	if source == nil {
		return content.Metadata{}, errors.New("filestore: nil source")
	}
	stageRel, err := stagePath()
	if err != nil {
		return content.Metadata{}, fmt.Errorf("filestore: staging name: %w", err)
	}
	w, err := s.stageOpen(stageRel)
	if err != nil {
		return content.Metadata{}, fmt.Errorf("filestore: create staging: %w", err)
	}
	st := stagedFile{w: w}
	// fail 把主因、关闭错误（若有）与清理错误合并上报；关闭恰好执行一次。
	// fail joins the primary cause, the close error (if any) and the cleanup
	// error; the staging handle is closed exactly once.
	fail := func(cause error) (content.Metadata, error) {
		return content.Metadata{}, s.cleanupStage(stageRel, errors.Join(cause, st.closeErrOnce()))
	}
	meta, err := content.Calculate(ctx, io.TeeReader(source, w))
	if err != nil {
		return fail(fmt.Errorf("filestore: hash source: %w", err))
	}
	if err := validateExpected(meta, expected); err != nil {
		return fail(err)
	}
	if err := st.closeErrOnce(); err != nil {
		// 关闭失败必须阻止发布；错误经 fail 保留（cause 为 nil 时只含关闭错误）。
		// A close failure must prevent publication; fail preserves it (with
		// a nil cause only the close error remains).
		return fail(nil)
	}
	if err := ctx.Err(); err != nil {
		return fail(fmt.Errorf("filestore: cancelled before publish: %w", err))
	}
	reused, err := s.publish(stageRel, meta.SHA1)
	if err != nil {
		return fail(err)
	}
	if reused {
		// 复用既存对象：本次 staging 也要清掉，失败则按错误上报零结果。
		// Reused an existing object: still drop this attempt's staging;
		// a cleanup failure is reported as an error with a zero result.
		if cerr := s.cleanupStage(stageRel, nil); cerr != nil {
			return content.Metadata{}, cerr
		}
	}
	return meta, nil
}

// stagedFile 记录暂存句柄与关闭状态，保证 Close 恰好执行一次并记住其结果。
// stagedFile tracks the staging handle and its close state so Close runs
// exactly once and its outcome is remembered.
type stagedFile struct {
	w        io.WriteCloser
	closed   bool
	closeErr error
}

// closeErrOnce 首次调用执行 Close 并缓存包装错误；之后是空操作并返回缓存。
// closeErrOnce performs Close on the first call and caches the wrapped
// error; later calls are no-ops returning the cached result.
func (st *stagedFile) closeErrOnce() error {
	if !st.closed {
		st.closed = true
		if err := st.w.Close(); err != nil {
			st.closeErr = fmt.Errorf("filestore: close staging: %w", err)
		}
	}
	return st.closeErr
}

// publish 在 staging 已校验关闭后执行发布：确保 0700 fanout（本地策略：拒绝
// 组/其他可写的 fanout，掩码 0022 与 root 一致），拒绝符号链接/非目录 fanout
// 与非普通最终对象；已存在
// 普通对象原样复用（返回 reused），否则仅一次同文件系统 Root.Rename，不做
// 投机恢复或拷贝兜底——正常首发者只创建、从不删除 fanout。
// publish runs after staged bytes are validated and closed: ensure a 0700
// fanout (local policy: group/other-writable fanouts rejected, mask 0022
// matching the root), reject symlink/
// non-directory fanouts and nonregular final objects; reuse an existing
// regular object as-is (returning reused), otherwise exactly one
// same-filesystem Root.Rename with no speculative recovery or copy
// fallback — normal first writers only ever create, never remove, fanouts.
func (s *Store) publish(stageRel, sha1 string) (bool, error) {
	rel, err := keyPath(sha1)
	if err != nil {
		return false, fmt.Errorf("filestore: publish: %w", err)
	}
	if err := s.ensureDir(rel[:2], "fanout", 0o022); err != nil {
		return false, err
	}
	fi, err := s.root.Lstat(rel)
	switch {
	case err == nil:
		if !fi.Mode().IsRegular() {
			return false, fmt.Errorf("filestore: existing object not regular: %w", ErrUnsafePath)
		}
		// 已存在普通对象：原样复用，不重哈希也不替换 inode。
		// Existing regular object: reuse as-is, no rehash or inode swap.
		return true, nil
	case !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("filestore: inspect object: %w", err)
	}
	if err := s.renameFile(stageRel, rel); err != nil {
		return false, fmt.Errorf("filestore: publish rename: %w", err)
	}
	return false, nil
}

// ensureDir 确保 root 下存在 0700 目录 name；已存在时拒绝符号链接、非目录与
// unsafeMask 命中的权限位（本地安全策略），不改变已有目录权限。
// ensureDir ensures a 0700 directory name under the root; existing symlinks,
// non-directories and permission bits hit by unsafeMask (local safety
// policy) are rejected and permissions are never changed.
func (s *Store) ensureDir(name, what string, unsafeMask os.FileMode) error {
	fi, err := s.root.Lstat(name)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("filestore: %s not a directory: %w", what, ErrUnsafePath)
		}
		if fi.Mode().Perm()&unsafeMask != 0 {
			return fmt.Errorf("filestore: %s permissions too broad: %w", what, ErrUnsafePath)
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		if err := s.root.Mkdir(name, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) { // 并发创建 / concurrent creation
				if fi, lerr := s.root.Lstat(name); lerr == nil && fi.IsDir() &&
					fi.Mode()&os.ModeSymlink == 0 && fi.Mode().Perm()&unsafeMask == 0 {
					return nil
				}
			}
			return fmt.Errorf("filestore: mkdir %s: %w", what, err)
		}
		return nil
	default:
		return fmt.Errorf("filestore: inspect %s: %w", what, err)
	}
}

// cleanupStage 删除本次 staging；删除失败与原始原因一并报告。
// cleanupStage removes this attempt's staging; removal failure is joined
// with the original cause.
func (s *Store) cleanupStage(stageRel string, cause error) error {
	if rerr := s.removeFile(stageRel); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return errors.Join(cause, fmt.Errorf("filestore: cleanup staging: %w", rerr))
	}
	return cause
}

// stagePath 生成 .staging/<crypto random> 相对路径。
// stagePath yields a .staging/<crypto random> root-relative path.
func stagePath() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(stageDirName, hex.EncodeToString(b[:]))), nil
}

// Open 读取既有对象：仅接受 keyPath 通过的规范 40 位小写 SHA-1；fanout 与
// 最终路径先 Lstat 拒绝符号链接/目录/FIFO/非普通对象，再用 O_NOFOLLOW|
// O_NONBLOCK 打开并复核普通文件。缺失对象错误满足 errors.Is(os.ErrNotExist)。
// Open reads an existing object: only a canonical 40-char lowercase SHA-1
// accepted via keyPath; fanout and final paths are Lstat-checked to reject
// symlinks/directories/FIFOs/nonregular files, then opened with
// O_NOFOLLOW|O_NONBLOCK and re-verified regular. Missing objects satisfy
// errors.Is(os.ErrNotExist).
func (s *Store) Open(ctx context.Context, sha1 string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("filestore: cancelled: %w", err)
	}
	rel, err := keyPath(sha1)
	if err != nil {
		return nil, err
	}
	fi, err := s.root.Lstat(rel[:2])
	if err != nil {
		return nil, fmt.Errorf("filestore: inspect fanout: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return nil, fmt.Errorf("filestore: fanout not a directory: %w", ErrUnsafePath)
	}
	// 本地安全策略（非目标权限对齐）：拒绝组/其他可写的 fanout（0022）。
	// Local safety policy (not target permissions parity): reject
	// group/other-writable fanouts (0022).
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("filestore: fanout writable by group/others: %w", ErrUnsafePath)
	}
	fi, err = s.root.Lstat(rel)
	if err != nil {
		return nil, fmt.Errorf("filestore: inspect object: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("filestore: object not regular: %w", ErrUnsafePath)
	}
	f, err := openNoFollow(s.root, rel)
	if err != nil {
		return nil, fmt.Errorf("filestore: open object: %w", err)
	}
	// 复核：O_NOFOLLOW 之外仍需确认是普通文件；os.Root 会跟随内部相对
	// 符号链接，不能假定它单独拒绝符号链接或绑定挂载。
	// Re-verify regularity beyond O_NOFOLLOW; os.Root follows inner
	// relative symlinks and must not be assumed to reject symlinks or
	// bindmounts by itself.
	if fi, err := f.Stat(); err != nil {
		f.Close()
		return nil, fmt.Errorf("filestore: stat object: %w", err)
	} else if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("filestore: object not regular: %w", ErrUnsafePath)
	}
	return f, nil
}

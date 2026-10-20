// Command binflow-contract runs the read-only generic download contract
// probe frozen in docs/GENERIC-DOWNLOAD-CONTRACT.md.
// binflow-contract 命令运行 GENERIC-DOWNLOAD-CONTRACT.md 冻结的只读下载契约探针。
//
// It issues only the frozen GET/HEAD cases, refuses redirects and proxies,
// keeps system TLS verification, reserves the JSON report exclusively before
// any network activity, and prints counts only: no URLs, credentials, file
// paths or response bodies appear on stdout/stderr.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/0ldlight/bf/internal/compatprobe"
)

const maxAuthFileBytes = 4096

const usageText = `binflow-contract: read-only generic download contract probe

Usage:
  binflow-contract -url ARTIFACT_URL -fixture-file FILE -report FILE
    [-authorization-file FILE] [-timeout DURATION]

Flags:
  -url string
        absolute artifact URL (http/https; no userinfo, query or fragment)
  -fixture-file path
        known synthetic fixture bytes, regular non-symlink file, 5..65536 bytes
  -authorization-file path
        optional single-line "Basic ..." or "Bearer ..." value in a regular
        non-symlink file, mode 0600 or stricter, outside the source repository
  -report path
        exclusive JSON report output; must not exist, created mode 0600
  -timeout duration
        per-request timeout, default 10s, maximum 60s

The probe is read-only (GET/HEAD only, no redirects, no proxy, system TLS
verification). Errors use static categories; stdout carries counts only.
Selecting the probe profile does not attest the target deployment version.
`

// run executes one CLI invocation and returns the process exit code. All
// diagnostics are static categories written to stderr; stdout carries the
// final counts line only.
// run 执行一次 CLI 调用并返回退出码；诊断一律为静态类别写 stderr，stdout 仅输出计数。
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("binflow-contract", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	artifactURL := fs.String("url", "", "absolute artifact URL")
	fixturePath := fs.String("fixture-file", "", "fixture bytes file")
	authPath := fs.String("authorization-file", "", "optional Authorization value file")
	reportPath := fs.String("report", "", "exclusive JSON report output path")
	timeout := fs.Duration("timeout", compatprobe.DefaultTimeout, "per-request timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usageText)
			return 0
		}
		fmt.Fprintln(stderr, "binflow-contract: invalid arguments (see -h)")
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "binflow-contract: unexpected positional arguments")
		return 2
	}
	if *artifactURL == "" || *fixturePath == "" || *reportPath == "" {
		fmt.Fprintln(stderr, "binflow-contract: -url, -fixture-file and -report are required")
		return 2
	}
	if *timeout <= 0 || *timeout > compatprobe.MaxTimeout {
		fmt.Fprintln(stderr, "binflow-contract: -timeout must be positive and at most 60s")
		return 2
	}

	fixture, err := readFixtureFile(*fixturePath)
	if err != nil {
		fmt.Fprintf(stderr, "binflow-contract: fixture file rejected: %v\n", err)
		return 2
	}
	authorization := ""
	if *authPath != "" {
		authorization, err = readAuthorizationFile(*authPath)
		if err != nil {
			fmt.Fprintf(stderr, "binflow-contract: authorization file rejected: %v\n", err)
			return 2
		}
	}
	if err := compatprobe.Validate(compatprobe.Options{
		URL: *artifactURL, Fixture: fixture, Authorization: authorization, Timeout: *timeout,
	}); err != nil {
		fmt.Fprintf(stderr, "binflow-contract: configuration rejected: %v\n", err)
		return 2
	}

	// Reserve the report exclusively before any network activity. O_EXCL
	// refuses existing files and symlinks, so the report path cannot alias
	// the fixture or authorization inputs and nothing is ever truncated.
	// 网络活动前独占预约报告；O_EXCL 拒绝既有文件与符号链接，不会截断任何内容。
	reportFile, err := os.OpenFile(*reportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(stderr, "binflow-contract: report path must not exist and must be creatable mode 0600")
		return 2
	}

	report, runErr := compatprobe.Run(context.Background(), compatprobe.Options{
		URL: *artifactURL, Fixture: fixture, Authorization: authorization, Timeout: *timeout,
	})

	// The structured report is persisted on success and on every failure
	// after reservation. Each failure category is reported independently, so
	// a report write failure is still surfaced when the probe also failed.
	// 预约成功后的任何失败都会持久化结构化报告；各类失败独立提示，
	// 即使探针同时失败，报告写入失败也照样显示。
	writeErr := writeReport(reportFile, report)
	closeErr := reportFile.Close()
	return finish(stdout, stderr, report, runErr, writeErr, closeErr)
}

// finish emits the static diagnostics and counts for a reserved-report run
// and picks the exit code. Probe failure and report-write failure are both
// reported, never one masking the other.
// finish 输出预约报告后的静态诊断与计数并决定退出码；探针失败与报告写入失败
// 同时提示，互不遮蔽。
func finish(stdout, stderr io.Writer, report compatprobe.Report, runErr, writeErr, closeErr error) int {
	if runErr != nil {
		fmt.Fprintf(stderr, "binflow-contract: probe failed: %v\n", runErr)
	}
	if writeErr != nil || closeErr != nil {
		fmt.Fprintln(stderr, "binflow-contract: report write failed")
	}
	printCounts(stdout, report)
	if runErr != nil || writeErr != nil || closeErr != nil {
		return 1
	}
	return 0
}

func writeReport(f *os.File, report compatprobe.Report) error {
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// printCounts summarizes one run without URLs, credentials or bodies.
// printCounts 仅输出计数，不含 URL、凭据或正文。
func printCounts(w io.Writer, report compatprobe.Report) {
	passed, failed, skipped := 0, 0, 0
	for _, c := range report.Cases {
		switch c.State {
		case compatprobe.StatePassed:
			passed++
		case compatprobe.StateFailed:
			failed++
		default:
			skipped++
		}
	}
	fmt.Fprintf(w, "binflow-contract: profile %s planned %d executed %d passed %d failed %d skipped %d\n",
		report.Profile, report.Planned, report.Executed, passed, failed, skipped)
}

// readFixtureFile loads bounded fixture bytes from a regular non-symlink
// file so FIFOs cannot hang and symlinks are never followed.
// readFixtureFile 从普通非符号链接文件读取有界制品字节，FIFO 不会阻塞。
func readFixtureFile(path string) ([]byte, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, errors.New("could not be opened without following symlinks")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, compatprobe.MaxFixtureBytes+1))
	if err != nil {
		return nil, errors.New("could not be read")
	}
	if len(data) > compatprobe.MaxFixtureBytes || len(data) < compatprobe.MinFixtureBytes {
		return nil, errors.New("size outside supported bounds")
	}
	return data, nil
}

// readAuthorizationFile enforces the credential hygiene rules: regular
// non-symlink file, mode exactly 0600 or 0400 (no owner execute, no group or
// world bits, no special permission bits), at most 4096 bytes, exactly one
// non-empty line (one optional trailing LF), placed outside any source
// repository. Supplying the flag with an empty value is rejected: anonymous
// access means omitting the flag entirely.
// readAuthorizationFile 强制凭据文件卫生：普通非符号链接、模式恰为 0600 或 0400
// （无属主执行位、无组/其他位、无特殊权限位）、至多 4096 字节、恰好一行非空
// （可选末尾 LF）、位于源码仓库之外。带标志但值为空一律拒绝：匿名访问应完全省略标志。
func readAuthorizationFile(path string) (string, error) {
	if err := ensureOutsideRepository(path); err != nil {
		return "", err
	}
	f, err := openNoFollow(path)
	if err != nil {
		return "", errors.New("could not be opened without following symlinks")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	mode := st.Mode()
	if mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return "", errors.New("mode must be 0600 or 0400 (no special permission bits)")
	}
	if perm := mode.Perm(); perm&^0o600 != 0 {
		return "", errors.New("mode must be 0600 or 0400 (no owner execute, group or world access)")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAuthFileBytes+1))
	if err != nil {
		return "", errors.New("could not be read")
	}
	if len(data) > maxAuthFileBytes {
		return "", errors.New("exceeds maximum size")
	}
	value := strings.TrimSuffix(string(data), "\n") // One optional trailing LF only / 仅允许一个末尾 LF。
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("must contain exactly one line")
	}
	if value == "" {
		return "", errors.New("must contain a non-empty Basic/Bearer line; omit the flag for anonymous access")
	}
	return value, nil
}

// ensureOutsideRepository rejects paths inside a directory tree carrying a
// .git marker (repository checkout or worktree file). The existing parent
// directory is resolved canonically first, so an outside symlink alias
// pointing into a nested repository directory cannot hide its ancestry; the
// leaf itself keeps O_NOFOLLOW protection at open time. The marker is
// checked with Lstat so dangling or unusual .git entries still count, and
// any lookup error other than nonexistence fails closed. It only inspects
// directory entries and never runs Git or a shell.
// ensureOutsideRepository 拒绝位于含 .git 标记（仓库检出或 worktree 文件）目录树内
// 的路径。先规范化已存在的父目录，使指向仓库嵌套目录的外部符号链接别名无法隐藏
// 祖先；末级路径在打开时仍受 O_NOFOLLOW 保护。标记用 Lstat 检查，悬空或异常的
// .git 条目同样计入；除不存在以外的查找错误一律拒绝。仅检查目录项，不执行 Git 或 shell。
func ensureOutsideRepository(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return errors.New("path could not be resolved")
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return errors.New("path could not be resolved")
	}
	for ; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return errors.New("must live outside the source repository")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("path could not be resolved")
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

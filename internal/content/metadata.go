// Package content 计算制品字节流的内容身份。
// Package content computes the content identity of artifact byte streams.
package content

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// readBufSize 限制单次读取的上界，流式处理不整体驻留内容。
// readBufSize bounds each streaming read so content is never held whole.
const readBufSize = 32 * 1024

// Metadata 是一段内容的身份与兼容摘要。
// SHA-1 is the content identity used for storage addressing; SHA-256 and
// MD5 are retained checksums for compatibility, not password hashing.
type Metadata struct {
	SHA1   string
	SHA256 string
	MD5    string
	Size   int64
}

// Calculate 流式消费 source 一次，返回 SHA-1/SHA-256/MD5 十六进制小写摘要与实际字节数。
// Calculate consumes the caller-owned source exactly once without closing
// it and returns the lowercase hex digests plus the number of bytes read.
// Success requires the reader to return the literal io.EOF; an error that
// merely wraps or joins io.EOF stays an error. Any read error or context
// cancellation yields a zero Metadata and an error wrapping the cause
// (errors.Is works). Cancellation is checked before the first read and
// between reads; a blocking io.Reader that never returns cannot be
// interrupted forcibly.
func Calculate(ctx context.Context, source io.Reader) (Metadata, error) {
	if source == nil {
		return Metadata{}, errors.New("content: nil source")
	}
	sha1Sum := sha1.New()
	sha256Sum := sha256.New()
	md5Sum := md5.New()
	var size int64
	buf := make([]byte, readBufSize)
	for {
		if err := ctx.Err(); err != nil {
			return Metadata{}, fmt.Errorf("content: cancelled: %w", err)
		}
		n, err := source.Read(buf)
		if n > 0 {
			sha1Sum.Write(buf[:n])
			sha256Sum.Write(buf[:n])
			md5Sum.Write(buf[:n])
			size += int64(n)
		}
		if err == io.EOF {
			return Metadata{
				SHA1:   hex.EncodeToString(sha1Sum.Sum(nil)),
				SHA256: hex.EncodeToString(sha256Sum.Sum(nil)),
				MD5:    hex.EncodeToString(md5Sum.Sum(nil)),
				Size:   size,
			}, nil
		}
		if err != nil {
			return Metadata{}, fmt.Errorf("content: read source: %w", err)
		}
	}
}

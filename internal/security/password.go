// BIN-201 有界口令编解码：只支持一个固定 Argon2id 画像，即实际打包组件
// ArgonEncoder(16,32,1,512,16) 经 TypeCodedHashEncoder 前缀产生的
// `argon$$argon2id$v=19$m=512,t=16,p=1$<salt>$<key>`（盐16字节、钥32字节、
// 无填充标准 Base64）。其他算法/参数/版本变体与畸形编码一律拒绝
// （ErrCredentialEncoding），无任意内存/时间参数；这比参考实现的宽松解析器
// 更窄，是显式本地画像，不声称 Access/FIPS/legacy 完全等价。明文与哈希
// 永不进入日志。约束来源：access.war 固定组件的 51 项断言参考证据
// （argon-reference/actual.tsv、verify.json）。
// Bounded credential codec supporting exactly one fixed Argon2id profile —
// the actual packaged ArgonEncoder(16,32,1,512,16) output prefixed by
// TypeCodedHashEncoder as `argon$$argon2id$v=19$m=512,t=16,p=1$<salt>$<key>`
// (16-byte salt, 32-byte key, unpadded standard Base64). Other algorithms,
// parameter/version variants and malformed encodings are rejected with
// ErrCredentialEncoding; no arbitrary memory/time parameters exist. This is
// deliberately narrower than the reference permissive parser: an explicit
// local profile, not full Access/FIPS/legacy parity. Plaintext and hashes
// never reach logs. Bounds are backed by the fixed access.war component's
// 51-assertion reference evidence (argon-reference/actual.tsv, verify.json).
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	// credentialPrefix 是唯一被接受的编码前缀（类型段 + Spring 参数段）。
	// credentialPrefix is the sole accepted encoding prefix (type segment
	// plus the Spring parameter block).
	credentialPrefix = "argon$$argon2id$v=19$m=512,t=16,p=1$"
	// argonTime/argonMemory/argonThreads/argonKeyLen 是固定 KDF 参数
	// （t=16、m=512KiB、p=1、keylen=32），无调用方可调输入。
	// argonTime/argonMemory/argonThreads/argonKeyLen are fixed KDF
	// parameters (t=16, m=512KiB, p=1, keylen=32) with no caller input.
	argonTime    = 16
	argonMemory  = 512
	argonThreads = 1
	argonKeyLen  = 32
	// argonSaltLen 是固定随机盐长度（16 字节）。
	// argonSaltLen is the fixed random salt length (16 bytes).
	argonSaltLen = 16
	// encodedSaltLen/encodedKeyLen 是无填充标准 Base64 的精确段长度
	// （16 字节→22 字符、32 字节→43 字符）；填充变体按长度拒绝。
	// encodedSaltLen/encodedKeyLen are the exact unpadded-standard-Base64
	// segment lengths (16 bytes → 22 chars, 32 bytes → 43 chars); padded
	// variants are rejected by length.
	encodedSaltLen = 22
	encodedKeyLen  = 43
	// maxRawCredentialBytes 限定原始口令输入：非空、有效 UTF-8、≤1024 字节。
	// maxRawCredentialBytes bounds raw credential input: nonempty, valid
	// UTF-8, at most 1024 bytes.
	maxRawCredentialBytes = 1024
)

// validRawCredential 只做输入边界判定：非空、有效 UTF-8、长度 ≤1024 字节；
// 不 trim、不归一化；Unicode/NUL/空格允许且逐字节保留，畸形 UTF-8 排除。
// 目标组件对 Java null/空串的观察不产生 Go 侧可空串语义。
// validRawCredential decides only the input bound: nonempty, valid UTF-8 and
// at most 1024 bytes; no trimming or normalization. Unicode/NUL/spaces are
// allowed and byte-preserved while malformed UTF-8 is excluded. The
// reference component's Java null/empty observations create no Go nullable
// string semantics.
func validRawCredential(raw string) bool {
	return raw != "" && len(raw) <= maxRawCredentialBytes && utf8.ValidString(raw)
}

// parseCredential 在任何 KDF 计算之前完成全部结构与长度校验：精确前缀、
// 余下按 `$` 恰好分为两段、两段各自精确 22/43 字符、严格无填充标准
// Base64 且解码长度恰为 16/32 字节。任何偏差返回 ErrCredentialEncoding，
// 无有限边界之外的解析路径（拒绝其他算法、参数/顺序/版本变体、
// `{argon}` 风格前缀与畸形编码）。
// parseCredential performs every structural and length check BEFORE any KDF
// work: exact prefix, the remainder splitting into exactly two `$` segments
// of exactly 22/43 characters, strict unpadded standard Base64 decoding to
// exactly 16/32 bytes. Any deviation returns ErrCredentialEncoding with no
// parsing path beyond these finite bounds (other algorithms, parameter/
// order/version variants, `{argon}`-style prefixes and malformed encodings
// are all rejected).
func parseCredential(encoded string) (salt, key []byte, err error) {
	if len(encoded) != len(credentialPrefix)+encodedSaltLen+1+encodedKeyLen || !strings.HasPrefix(encoded, credentialPrefix) {
		return nil, nil, ErrCredentialEncoding
	}
	rest := encoded[len(credentialPrefix):]
	parts := strings.Split(rest, "$")
	if len(parts) != 2 || len(parts[0]) != encodedSaltLen || len(parts[1]) != encodedKeyLen {
		return nil, nil, ErrCredentialEncoding
	}
	salt, err = base64.RawStdEncoding.Strict().DecodeString(parts[0])
	if err != nil || len(salt) != argonSaltLen {
		return nil, nil, ErrCredentialEncoding
	}
	key, err = base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(key) != argonKeyLen {
		return nil, nil, ErrCredentialEncoding
	}
	return salt, key, nil
}

// deriveCredential 以固定参数运行 Argon2id；调用方必须已通过
// validRawCredential 与 parseCredential 的边界校验。
// deriveCredential runs Argon2id with the fixed parameters; the caller must
// have passed the validRawCredential and parseCredential bounds.
func deriveCredential(raw string, salt []byte) []byte {
	return argon2.IDKey([]byte(raw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}

// GenerateCredential 把输入当原始口令哈希：生成 16 字节 crypto/rand 盐、
// 派生 32 字节钥并输出规范前缀编码。输入不满足有限边界（空/畸形 UTF-8/
// >1024 字节）返回 ErrInvalidCredentials；形似已编码的输入同样按原始
// 口令处理，不自动识别或导入已编码哈希。
// GenerateCredential hashes its input as a RAW passphrase: a 16-byte
// crypto/rand salt is drawn, a 32-byte key derived, and the canonical
// prefixed encoding returned. Input outside the finite bounds (empty,
// malformed UTF-8, >1024 bytes) returns ErrInvalidCredentials. Input that
// merely looks encoded is still hashed as raw; trusted encoded import is
// never automatic.
func GenerateCredential(raw string) (string, error) {
	return generateCredential(rand.Reader, raw)
}

// generateCredential 是接受注入随机源的私有形态，仅供同包故障注入测试
// 使用；r 读满 16 字节失败时返回固定文案错误，不回显任何输入。
// generateCredential is the private shape accepting an injected entropy
// source for same-package fault-injection tests only; failure to read 16
// bytes from r returns a fixed-text error echoing no input.
func generateCredential(r io.Reader, raw string) (string, error) {
	if !validRawCredential(raw) {
		return "", ErrInvalidCredentials
	}
	salt := make([]byte, argonSaltLen)
	if _, err := io.ReadFull(r, salt); err != nil {
		return "", fmt.Errorf("%w: credential salt unavailable", ErrConfiguration)
	}
	key := deriveCredential(raw, salt)
	var b strings.Builder
	b.Grow(len(credentialPrefix) + encodedSaltLen + 1 + encodedKeyLen)
	b.WriteString(credentialPrefix)
	b.WriteString(base64.RawStdEncoding.EncodeToString(salt))
	b.WriteByte('$')
	b.WriteString(base64.RawStdEncoding.EncodeToString(key))
	return b.String(), nil
}

// CheckCredential 校验原始口令与已存储编码。raw 先做有限边界判定：无效
// 输入直接返回 (false, nil)，不触碰 encoded；有效 raw 配畸形/不支持的
// 存储编码返回 (false, ErrCredentialEncoding)；否则固定参数派生并以
// crypto/subtle 常数时间比较。不承诺网络时序等价。
// CheckCredential verifies a raw passphrase against a stored encoding. The
// raw bound is checked first: invalid input returns (false, nil) without
// touching encoded; valid raw with a malformed or unsupported stored
// encoding returns (false, ErrCredentialEncoding); otherwise the fixed
// parameters derive a key compared with a crypto/subtle constant-time
// comparison. No network timing equality is promised.
func CheckCredential(raw, encoded string) (bool, error) {
	if !validRawCredential(raw) {
		return false, nil
	}
	salt, key, err := parseCredential(encoded)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(deriveCredential(raw, salt), key) == 1, nil
}

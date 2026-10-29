// 本文件是 BIN-201 UNIT 独立行为契约：仅测试 internal/security 冻结 API
// （model.go 定义的 Backend/HTTPConfig、password.go 的 Generate/Check、
// store.go 的 NewStore/SchemaSQL、http.go 的 NewAuthenticatedDownload）与
// 真实 JVM Argon 参考向量。同一包允许构造测试专用 Principal 与访问
// authenticatedDownload 私有时钟；产品代码不得新增任何铸造入口。
// This file is the BIN-201 UNIT behavioral contract: it exercises only the
// frozen internal/security API (Backend/HTTPConfig from model.go, the
// credential codec, NewStore/SchemaSQL and NewAuthenticatedDownload) plus
// genuine JVM Argon reference vectors. Same-package tests may construct
// test-only Principals and reach the authenticatedDownload private clock;
// no production minting path may be added for them.
package security

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// suRepoKey 是本套件唯一配置的仓库键；suChallenge 是固定注入的入口
// challenge。两者都不是目标产品的冻结常量。
// suRepoKey is the suite's single configured repository key and
// suChallenge the injected entry challenge; neither is a frozen product
// constant.
const (
	suRepoKey   = "su.repo-1"
	suChallenge = `Basic realm="BinFlow Unit"`
	suOtherKey  = "su.other-2"
)

// suArgonPrefix 是唯一受支持存储画像的固定前缀（含参数段）。
// suArgonPrefix is the fixed prefix (parameters included) of the only
// supported storage profile.
const suArgonPrefix = "argon$$argon2id$v=19$m=512,t=16,p=1$"

// suUser/suPassword 是受控合成测试身份，不对应任何真实账户。
// suUser/suPassword are controlled synthetic identities, not accounts.
const (
	suUser     = "user1"
	suUserID   = int64(7)
	suPassword = "Unit-Raw-Password-42"
)

// ---------------------------------------------------------------------------
// Codec：真实 JVM 参考向量 / Codec: genuine JVM reference vectors
// ---------------------------------------------------------------------------

// suCodecVectors 转录自 .binflow/tasks/BIN-201-SECURITY/argon-reference/
// actual.tsv（固定 access.war 组件 7.191.16 的实际 ArgonEncoder 探针，
// 51 项断言）。raw/enc 均保留 base64 形式以便逐字节核对，运行时解码；
// enc 是 Spring 形 "$argon2id$..."，本产品存储形需再加 "argon$" 前缀。
// suCodecVectors transcribes actual.tsv from the fixed-WAR ArgonEncoder
// probe (51 assertions). raw/enc stay base64 for byte-exact auditing and
// are decoded at runtime; enc is the Spring form "$argon2id$...", the
// product storage form prepends "argon$".
var suCodecVectors = []struct {
	name    string
	rawB64  string
	encB64  string
	wantOK  bool
	wantErr error
}{
	{"same-0", "Q29udHJvbGxlZC1BU0NJSS0xMjMh", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkWDFVNGJWOHYyMlpKZ1YxNEJKY0dxUSRaUHB6Y2pZUGpUeFFMUCtmbndYeW9LUTllUjBxVzJpTmRyakx1dEV6QUJv", true, nil},
	{"different-0", "Q29udHJvbGxlZC1BU0NJSS0xMjMhIQ==", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkWDFVNGJWOHYyMlpKZ1YxNEJKY0dxUSRaUHB6Y2pZUGpUeFFMUCtmbndYeW9LUTllUjBxVzJpTmRyakx1dEV6QUJv", false, nil},
	{"same-1", "5a+G56CBLcOpLfCfmIA=", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkK0JYVHBlaCtVeHJmb0dNcUt4c1pHQSRtTWN0akVhS25nWTJlQkFrYTV0Ujl3b1NmVFcwRVpyWWpTRmhPR3h5YVNV", true, nil},
	{"different-1", "5a+G56CBLcOpLfCfmIAh", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkK0JYVHBlaCtVeHJmb0dNcUt4c1pHQSRtTWN0akVhS25nWTJlQkFrYTV0Ujl3b1NmVFcwRVpyWWpTRmhPR3h5YVNV", false, nil},
	{"same-4", "ICBwYWRkZWQgIA==", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkSXEzZU0rU1Y2NmpEd2R3UjBSS3crZyRxdkkxcXAwNUxENWlKL2dHbVNjeEs3d1pOUmxYZ255emtXWVZwbVczUmlN", true, nil},
	{"trim-sensitive", "cGFkZGVk", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkSXEzZU0rU1Y2NmpEd2R3UjBSS3crZyRxdkkxcXAwNUxENWlKL2dHbVNjeEs3d1pOUmxYZ255emtXWVZwbVczUmlN", false, nil},
	{"same-5", "YQBi", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkRllibTdKMUtnV0IrN3FFQXhWa1FoQSRydzBpZEMrMFRLNWF3alhoU04yRmZsVzRyZ2RXUFNKc3RsSStSeEp3aUxv", true, nil},
	{"nul-sensitive", "YWI=", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkRllibTdKMUtnV0IrN3FFQXhWa1FoQSRydzBpZEMrMFRLNWF3alhoU04yRmZsVzRyZ2RXUFNKc3RsSStSeEp3aUxv", false, nil},
	{"same-6", "w6k=", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkRUMrVmp6bkVtZWNNUkhSTS9ucTNEdyRPZk1qQmFpUnlWNnIyQ2xYejN5UU9WbldJcmRKYlFWM3ZjeFZSWjNhb1g4", true, nil},
	{"different-6", "w6kh", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkRUMrVmp6bkVtZWNNUkhSTS9ucTNEdyRPZk1qQmFpUnlWNnIyQ2xYejN5UU9WbldJcmRKYlFWM3ZjeFZSWjNhb1g4", false, nil},
	{"same-7", "ZcyB", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkS2xYa1dISmZvbitDZ3hYeWw4b0NadyQrUndxVlhjNW9xeWo5dmkzaW10MVllVmZBTHdXUXNRMkpiN1dJbUdiRWJn", true, nil},
	{"different-7", "ZcyBIQ==", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkS2xYa1dISmZvbitDZ3hYeWw4b0NadyQrUndxVlhjNW9xeWo5dmkzaW10MVllVmZBTHdXUXNRMkpiN1dJbUdiRWJn", false, nil},
	{"normalization-sensitive", "ZcyB", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkRUMrVmp6bkVtZWNNUkhSTS9ucTNEdyRPZk1qQmFpUnlWNnIyQ2xYejN5UU9WbldJcmRKYlFWM3ZjeFZSWjNhb1g4", false, nil},
	{"second-same", "Q29udHJvbGxlZC1BU0NJSS0xMjMh", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkUlRTdG5kSjQzR245Y1RVbWhkSlNHZyRUNTd4TmRWelZlY1dOdEg4OHF4a3ZQKzcyTWpOVkphOHJqUWpGRXduLzBv", true, nil},
	{"same-2", "", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkTmE0YzhJMWJWR1lBOGlmUzBodWxpZyQ5M0dRUDNUWDFzR0RUTTFZL1BvWEZsYkk0eHNXMjhob1RCNGtHVlY0bDRZ", false, nil},
	{"same-3", "eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eA==", "JGFyZ29uMmlkJHY9MTkkbT01MTIsdD0xNixwPTEkQ0tHcEl4bjB0UE9ndlhLNnRJM253QSRRMjhqSVFtcEN4M3YvTkNQK0I3dXI5L0NHRXRLTnI1eFRxbElwTWY5b01n", true, nil},
}

// suDecodeB64 严格标准 base64 解码辅助（向量均为合法填充形）。
// suDecodeB64 decodes strict standard base64 (vectors use padded form).
func suDecodeB64(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("test vector base64 decode failed: %v", err)
	}
	return raw
}

// TestSecurityCheckCredentialNativeVectors 断言本实现与真实 JVM 组件逐
// 字节等价：Unicode/NUL/空白原样保真，不 trim、不归一化、不做大小写折叠。
// 不同盐的同 raw 均通过、篡改 raw 均拒绝。错误通道必须为 nil（拒绝≠编码错）。
// TestSecurityCheckCredentialNativeVectors asserts byte-exact agreement with
// the real JVM component: Unicode/NUL/whitespace pass through without
// trimming, normalization or case folding; both salts accept the same raw
// while any altered raw is denied; denials use the nil error channel.
func TestSecurityCheckCredentialNativeVectors(t *testing.T) {
	for _, v := range suCodecVectors {
		t.Run(v.name, func(t *testing.T) {
			raw := string(suDecodeB64(t, v.rawB64))
			stored := "argon$" + string(suDecodeB64(t, v.encB64))
			ok, err := CheckCredential(raw, stored)
			if ok != v.wantOK {
				t.Fatalf("CheckCredential ok = %v, want %v (err=%v)", ok, v.wantOK, err)
			}
			if !errors.Is(err, v.wantErr) {
				t.Fatalf("CheckCredential err = %v, want %v", err, v.wantErr)
			}
		})
	}
}

// TestSecurityCheckCredentialMalformedEncodings 断言畸形/越界存储编码一律
// (false, ErrCredentialEncoding)：含真实 JVM malformed 系列与本地图谱边界
// （盐/钥长度、参数顺序、算法变体、填充、URL-safe 字符、bcrypt 前缀）。
// Spring 对 malformed-3（前缀杂讯）宽松接受；本产品显式收窄为拒绝，这是
// 有记录的本地政策差异，不是目标完整解析器等价性声明。
// TestSecurityCheckCredentialMalformedEncodings requires (false,
// ErrCredentialEncoding) for every malformed or out-of-profile stored
// encoding: the genuine JVM malformed series plus local profile edges
// (salt/key lengths, parameter order, algorithm variants, padding, URL-safe
// alphabet, bcrypt prefix). Spring accepts malformed-3 (prefix noise)
// permissively; this product explicitly narrows that to rejection — a
// recorded local deviation, not a claim of full parser parity.
func TestSecurityCheckCredentialMalformedEncodings(t *testing.T) {
	const goodSpring = "$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"
	salt20 := base64.RawStdEncoding.EncodeToString(make([]byte, 15))
	salt23 := base64.RawStdEncoding.EncodeToString(make([]byte, 17))
	key42 := base64.RawStdEncoding.EncodeToString(make([]byte, 31))
	key44 := base64.RawStdEncoding.EncodeToString(make([]byte, 33))
	malformed := []struct {
		name string
		enc  string
	}{
		{"jvm empty(null)", ""},
		{"jvm garbage", "garbage"},
		{"jvm malformed-3 prefix noise", "{argon}$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"jvm malformed-4 argon2i", "argon$$argon2i$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"jvm malformed-5 v=18", "argon$$argon2id$v=18$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"jvm malformed-6 m=bad", "argon$$argon2id$v=19$m=bad,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"jvm malformed-7 short key", "argon$$argon2id$v=19$m=512,t=16,p=1$AA$AA"},
		{"jvm malformed-8 invalid base64", "argon$$argon2id$v=19$m=512,t=16,p=1$***$***"},
		{"jvm malformed-9 missing key", "argon$$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ"},
		{"bare spring form no argon prefix", goodSpring},
		{"bcrypt prefix", "bcrypt$2a$10$abcdefghijklmnopqrstuu$01234567890123456789012345678901234567890123456789012345678901234567890123456789012"},
		{"argon2d variant", "argon$$argon2d$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"spring memory 4096", "argon$$argon2id$v=19$m=4096,t=3,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"spring v5_8 16384/2", "argon$$argon2id$v=19$m=16384,t=2,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"parallelism 2", "argon$$argon2id$v=19$m=512,t=16,p=2$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"missing p param", "argon$$argon2id$v=19$m=512,t=16$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"param order reordered", "argon$$argon2id$v=19$p=1,m=512,t=16$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"salt 15 bytes", "argon$$argon2id$v=19$m=512,t=16,p=1$" + salt20 + "$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"salt 17 bytes", "argon$$argon2id$v=19$m=512,t=16,p=1$" + salt23 + "$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"key 31 bytes", "argon$$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$" + key42},
		{"key 33 bytes", "argon$$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$" + key44},
		{"padded salt", "argon$$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ==$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"urlsafe salt", "argon$$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGq-$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"empty salt", "argon$$argon2id$v=19$m=512,t=16,p=1$$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
		{"empty key", "argon$$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$"},
		{"salt newline fixed length", "argon$" + strings.Replace(goodSpring, "X1U4", "X\nU4", 1)},
		{"key carriage return fixed length", "argon$" + strings.Replace(goodSpring, "ZPpz", "Z\rpz", 1)},
		{"nonzero trailing key bits", "argon$" + strings.TrimSuffix(goodSpring, "o") + "p"},
		{"oversized suffix", "argon$" + goodSpring + strings.Repeat("$", 10000)},
		{"extra segment", suArgonPrefix + "X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo$zz"},
		{"truncated prefix", "argon$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"},
	}
	for _, m := range malformed {
		t.Run(m.name, func(t *testing.T) {
			ok, err := CheckCredential("Controlled-ASCII-123!", m.enc)
			if ok {
				t.Fatalf("malformed encoding accepted: %q", m.enc)
			}
			if !errors.Is(err, ErrCredentialEncoding) {
				t.Fatalf("err = %v, want ErrCredentialEncoding", err)
			}
		})
	}
}

// TestSecurityCheckCredentialInvalidRawShortCircuit 断言非法 raw（空、
// 超长、畸形 UTF-8）在任何存储编码之前被拒绝为 (false,nil)：即使编码同样
// 畸形也不产生 ErrCredentialEncoding，证明先校验 raw 后解析编码的顺序。
// TestSecurityCheckCredentialInvalidRawShortCircuit requires invalid raw
// values (empty, overlong, malformed UTF-8) to answer (false,nil) regardless
// of the stored encoding — even a malformed encoding yields no
// ErrCredentialEncoding, proving raw validation precedes encoding parsing.
func TestSecurityCheckCredentialInvalidRawShortCircuit(t *testing.T) {
	validSpring := "$argon2id$v=19$m=512,t=16,p=1$X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"
	overlong := strings.Repeat("a", 1025)
	invalidUTF8 := "\xff\xfe"
	for _, raw := range []string{"", overlong, invalidUTF8} {
		for name, enc := range map[string]string{
			"valid encoding":   "argon$" + validSpring,
			"malformed encode": "garbage",
		} {
			ok, err := CheckCredential(raw, enc)
			if ok || err != nil {
				t.Fatalf("CheckCredential(%d bytes, %s) = (%v,%v), want (false,nil)", len(raw), name, ok, err)
			}
		}
	}
}

// TestSecurityGenerateCredentialCanonicalForm 断言生成器只产出唯一受支持
// 画像：精确前缀、总长 102（36 前缀+22 盐+1 分隔+43 钥）、7 段、无填充
// 标准 base64 盐/钥，且生成结果可回验、篡改 raw 即拒绝。1024 字节 raw 在
// 边界内（对齐 JVM encode-3 向量长度）。
// TestSecurityGenerateCredentialCanonicalForm pins the generator to the one
// supported profile: exact prefix, total length 102 (36 prefix + 22 salt + 1
// separator + 43 key), 7 segments, unpadded standard-base64 salt/key, round
// trip verification, and rejection of altered raw. The 1024-byte raw sits on
// the admitted boundary (JVM encode-3 vector length).
func TestSecurityGenerateCredentialCanonicalForm(t *testing.T) {
	raws := []string{
		"Controlled-ASCII-123!",
		string(suDecodeB64(t, "5a+G56CBLcOpLfCfmIA=")), // 密码-é-😀 / unicode
		string(suDecodeB64(t, "ZcyB")),                 // 分解形 é / decomposed
		"a\x00b",
		"  padded  ",
		strings.Repeat("x", 1024),
	}
	for _, raw := range raws {
		enc, err := GenerateCredential(raw)
		if err != nil {
			t.Fatalf("GenerateCredential(%d bytes) error = %v", len(raw), err)
		}
		if !strings.HasPrefix(enc, suArgonPrefix) {
			t.Fatalf("prefix = %q, want %q", enc[:min(len(enc), len(suArgonPrefix))], suArgonPrefix)
		}
		if got := len(enc); got != 102 {
			t.Fatalf("encoded length = %d, want 102", got)
		}
		segs := strings.Split(enc, "$")
		if len(segs) != 7 {
			t.Fatalf("segment count = %d, want 7", len(segs))
		}
		salt, key := segs[5], segs[6]
		if len(salt) != 22 || len(key) != 43 {
			t.Fatalf("salt/key length = %d/%d, want 22/43", len(salt), len(key))
		}
		if strings.ContainsAny(enc[36:], "=") {
			t.Fatalf("padded base64 emitted: %q", enc)
		}
		ok, err := CheckCredential(raw, enc)
		if err != nil || !ok {
			t.Fatalf("round trip failed: ok=%v err=%v", ok, err)
		}
		ok, err = CheckCredential(raw+"x", enc)
		if err != nil || ok {
			t.Fatalf("altered raw accepted: ok=%v err=%v", ok, err)
		}
	}
}

// TestSecurityGenerateCredentialInvalidRaw 断言生成器对空/超长/畸形 UTF-8
// raw 返回 ErrInvalidCredentials，绝不产出编码。
// TestSecurityGenerateCredentialInvalidRaw requires ErrInvalidCredentials
// (and no encoding) for empty, overlong and malformed-UTF-8 raw values.
func TestSecurityGenerateCredentialInvalidRaw(t *testing.T) {
	for _, raw := range []string{"", strings.Repeat("a", 1025), "\xff"} {
		enc, err := GenerateCredential(raw)
		if enc != "" || !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("GenerateCredential(%d bytes) = (%q,%v), want empty encoding and ErrInvalidCredentials", len(raw), enc, err)
		}
	}
}

// TestSecurityGenerateCredentialRandomSaltAndRawPassthrough 断言两次生成
// 使用不同随机盐，且 Generate 永远把输入当 raw 处理：输入本身形似编码串
// 也会被重新哈希（生成结果不等于输入、且以输入为 raw 回验通过），杜绝
// 编码串透传。
// TestSecurityGenerateCredentialRandomSaltAndRawPassthrough checks fresh
// random salts per call and that Generate always treats input as raw: an
// input shaped like an encoding string is re-hashed (output differs and
// verifies with the input as raw), eliminating pass-through.
func TestSecurityGenerateCredentialRandomSaltAndRawPassthrough(t *testing.T) {
	const raw = "Controlled-ASCII-123!"
	a, err := GenerateCredential(raw)
	if err != nil {
		t.Fatalf("first GenerateCredential error = %v", err)
	}
	b, err := GenerateCredential(raw)
	if err != nil {
		t.Fatalf("second GenerateCredential error = %v", err)
	}
	if a == b {
		t.Fatalf("identical encodings from two generations: %q", a)
	}
	if strings.Split(a, "$")[5] == strings.Split(b, "$")[5] {
		t.Fatalf("identical salts across generations")
	}
	// 输入形似编码仍按 raw 哈希 / encode-shaped input is still hashed as raw.
	looksEncoded, err := GenerateCredential(a)
	if err != nil {
		t.Fatalf("GenerateCredential(encode-shaped) error = %v", err)
	}
	if looksEncoded == a {
		t.Fatalf("encode-shaped input passed through unhashed")
	}
	ok, err := CheckCredential(a, looksEncoded)
	if err != nil || !ok {
		t.Fatalf("encode-shaped raw round trip failed: ok=%v err=%v", ok, err)
	}
}

// ---------------------------------------------------------------------------
// Principal / NewStore / SchemaSQL
// ---------------------------------------------------------------------------

// TestSecurityPrincipalZeroValue 冻结零值无效契约与同包测试铸造边界：
// 零 Principal 的 ID/Username 为零值；测试可用私有字段铸造（仅测试），方法
// 只读返回。产品不得因此新增导出铸造器。
// TestSecurityPrincipalZeroValue freezes the invalid-zero contract and the
// same-package test-only minting boundary: the zero Principal reports zero
// fields; tests may mint via private fields (tests only) and the methods are
// read-only. No exported minter may appear in production for this.
func TestSecurityPrincipalZeroValue(t *testing.T) {
	var zero Principal
	if zero.ID() != 0 || zero.Username() != "" {
		t.Fatalf("zero Principal = (%d,%q)", zero.ID(), zero.Username())
	}
	p := Principal{id: suUserID, username: suUser}
	if p.ID() != suUserID || p.Username() != suUser {
		t.Fatalf("minted Principal = (%d,%q)", p.ID(), p.Username())
	}
}

// suStubConnector 提供绝不触网的惰性 database/sql 后端：构造器不得校验
// schema 或联系数据库，任何意外连接都会被拒绝记录。
// suStubConnector supplies an inert database/sql backend: constructors must
// not validate schema or contact the database, and any unexpected connect is
// rejected on the spot.
type suStubConnector struct{ connected bool }

type suStubDriver struct{}
type suStubConn struct{}

func (c *suStubConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("security-test: unexpected prepare")
}
func (c *suStubConn) Close() error { return nil }
func (c *suStubConn) Begin() (driver.Tx, error) {
	return nil, errors.New("security-test: unexpected begin")
}

func (d suStubDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("security-test: unexpected open")
}

func (c *suStubConnector) Connect(context.Context) (driver.Conn, error) {
	c.connected = true
	return &suStubConn{}, nil
}
func (c *suStubConnector) Driver() driver.Driver { return suStubDriver{} }

// suTestDB 返回惰性 *sql.DB；测试结束时关闭。
// suTestDB returns an inert *sql.DB closed by the test cleanup.
func suTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&suStubConnector{})
	t.Cleanup(func() { db.Close() })
	return db
}

// TestSecurityNewStoreValidatesInputs 冻结构造器边界：nil db、timeout<=0、
// timeout>1min 一律失败；合法输入构造成功且绝不联系数据库。
// TestSecurityNewStoreValidatesInputs freezes constructor bounds: nil db,
// timeout<=0 and timeout>1min fail; valid input succeeds without contacting
// the database.
func TestSecurityNewStoreValidatesInputs(t *testing.T) {
	db := suTestDB(t)
	bad := []struct {
		name    string
		db      *sql.DB
		timeout time.Duration
	}{
		{"nil db", nil, time.Second},
		{"zero timeout", db, 0},
		{"negative timeout", db, -time.Second},
		{"timeout above one minute", db, time.Minute + time.Nanosecond},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			store, err := NewStore(c.db, c.timeout)
			if store != nil || err == nil {
				t.Fatalf("NewStore(%v,%v) = (%v,%v), want (nil,error)", c.db != nil, c.timeout, store != nil, err)
			}
		})
	}
	good, err := NewStore(db, time.Second)
	if err != nil || good == nil {
		t.Fatalf("NewStore(valid) = (%v,%v), want nonnil store", good, err)
	}
	if db.Stats().OpenConnections != 0 {
		t.Fatalf("constructor contacted the database")
	}
}

// TestSecuritySchemaSQLContract 冻结显式引导契约：包含全部六张表与版本
// CHECK；无 IF NOT EXISTS；除版本行外无任何 INSERT（不自动建管理员/授权）。
// TestSecuritySchemaSQLContract freezes the explicit bootstrap contract: all
// six tables plus the version CHECK exist, no IF NOT EXISTS appears, and no
// INSERT exists beyond the version row (no implicit admin/grant creation).
func TestSecuritySchemaSQLContract(t *testing.T) {
	if strings.TrimSpace(SchemaSQL) == "" {
		t.Fatal("SchemaSQL is empty")
	}
	for _, table := range []string{
		"security_schema_version", "security_users", "security_groups",
		"security_group_members", "security_repositories", "security_read_grants",
	} {
		if !strings.Contains(SchemaSQL, table) {
			t.Fatalf("SchemaSQL missing table %s", table)
		}
	}
	sqlText := regexp.MustCompile(`(?m)--[^\n]*`).ReplaceAllString(SchemaSQL, "")
	if !regexp.MustCompile(`version\s*=\s*1`).MatchString(sqlText) {
		t.Fatal("SchemaSQL missing version=1 CHECK")
	}
	if matched, _ := regexp.MatchString(`(?i)if\s+not\s+exists`, sqlText); matched {
		t.Fatal("SchemaSQL contains IF NOT EXISTS")
	}
	for _, table := range []string{
		"security_users", "security_groups", "security_group_members",
		"security_repositories", "security_read_grants",
	} {
		pattern := `(?i)insert\s+into\s+` + table
		if matched, _ := regexp.MatchString(pattern, sqlText); matched {
			t.Fatalf("SchemaSQL inserts into %s", table)
		}
	}
	if matched, _ := regexp.MatchString(`(?i)insert\s+into\s+security_schema_version`, sqlText); !matched {
		t.Fatal("SchemaSQL missing the version row insert")
	}
}

// ---------------------------------------------------------------------------
// HTTP 入口测试替身 / HTTP entry test peers
// ---------------------------------------------------------------------------

// suBackend 是测试专用 Backend 替身：记录调用顺序与收到的事实，回放注入
// 的结果/错误。它只实现冻结接口，不是产品认证器。
// suBackend is the test-only Backend peer: it records call order and received
// facts while replaying injected results/errors. It implements the frozen
// interface only and is not a product authenticator.
type suBackend struct {
	mu sync.Mutex

	validateErr error

	// authPrincipal 非 zero 时回放固定 principal（含畸形用例）；否则口令
	// 正确时动态铸造规范 principal，口令错误返回 ErrInvalidCredentials。
	// noVerify 关闭口令校验（零 principal 防御用例专用）。
	// A nonzero authPrincipal replays that fixed principal (including the
	// malformed cases); otherwise the valid password mints a canonical
	// principal dynamically and a wrong password yields
	// ErrInvalidCredentials. noVerify disables the password check for the
	// zero-principal defense case only.
	authFn        func(string, string) (Principal, error)
	readFn        func(Principal, RepositoryInfo, repository.Path) (bool, error)
	events        *suEvents
	authPrincipal Principal
	zeroPrincipal bool
	noVerify      bool
	authErr       error
	authCalls     int
	lastUser      string
	lastPassword  string

	repos    map[string]RepositoryInfo
	lookupFn func(string) (RepositoryInfo, error)
	lookups  int
	lastKey  string

	canRead    bool
	canReadErr error
	readCalls  int
	lastReader Principal
	lastRepo   RepositoryInfo
	lastPath   repository.Path

	order []string
}

func (b *suBackend) Validate(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.order = append(b.order, "validate")
	return b.validateErr
}

// suNormUser 镜像冻结的受控归一化：仅 ASCII 界空白裁剪与 A-Z 折叠。
// suNormUser mirrors the frozen controlled normalization: ASCII boundary
// whitespace trimming and A-Z folding only.
func suNormUser(username string) string {
	return strings.ToLower(strings.Trim(username, " \t"))
}

func (b *suBackend) Authenticate(_ context.Context, username, password string) (Principal, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.order = append(b.order, "authenticate")
	b.authCalls++
	b.events.add("authenticate")
	b.lastUser = username
	b.lastPassword = password
	if b.authErr != nil {
		return Principal{}, b.authErr
	}
	if !b.noVerify && password != suPassword {
		return Principal{}, ErrInvalidCredentials
	}
	if b.authFn != nil {
		return b.authFn(username, password)
	}
	if b.zeroPrincipal {
		return Principal{}, nil
	}
	if b.authPrincipal != (Principal{}) {
		return b.authPrincipal, nil
	}
	return Principal{id: suUserID, username: suNormUser(username)}, nil
}

func (b *suBackend) LookupRepository(_ context.Context, key string) (RepositoryInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.order = append(b.order, "catalog")
	b.lookups++
	b.events.add("catalog")
	b.lastKey = key
	if b.lookupFn != nil {
		return b.lookupFn(key)
	}
	info, ok := b.repos[key]
	if !ok {
		return RepositoryInfo{}, ErrRepositoryAbsent
	}
	return info, nil
}

func (b *suBackend) CanRead(_ context.Context, p Principal, info RepositoryInfo, path repository.Path) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.order = append(b.order, "policy")
	b.readCalls++
	b.events.add("policy")
	b.lastReader = p
	b.lastRepo = info
	b.lastPath = path
	if b.readFn != nil {
		return b.readFn(p, info, path)
	}
	return b.canRead, b.canReadErr
}

func (b *suBackend) snapshot() (authCalls, lookups, readCalls int, order []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.authCalls, b.lookups, b.readCalls, append([]string(nil), b.order...)
}

// suCounterStore 记录节点查询并按请求路径即时构造合法投影源，或回放
// 缺席/错误；目录缺席与节点缺席由此可区分。
// suCounterStore records node lookups and builds a valid projection source
// for the requested path on the fly, or replays absence/error; catalog
// absence and node absence stay distinguishable through it.
type suCounterStore struct {
	events *suEvents
	mu     sync.Mutex
	calls  int
	err    error
	absent bool
}

func (s *suCounterStore) LookupSnapshotSource(_ context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.events.add("node")
	if s.err != nil {
		return repository.FileSnapshotSource{}, s.err
	}
	if s.absent {
		return repository.FileSnapshotSource{}, sql.ErrNoRows
	}
	meta := repository.NewFileMetadata(repository.FileMetadataInput{ModifiedMillis: 4102444800000})
	return repository.FileSnapshotSource{
		Reference: repository.FileReference{
			ID:       71,
			Path:     path,
			Content:  suPayloadMeta(),
			Metadata: &meta,
		},
		SHA1ActualValid:   true,
		MD5ActualValid:    true,
		SHA256ActualValid: true,
	}, nil
}

func (s *suCounterStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// suPayload 是受控合成制品字节；suPayloadMeta 返回其真实摘要元数据。
// suPayload is the controlled synthetic artifact; suPayloadMeta returns its
// genuine digest metadata.
var suPayload = []byte("BinFlow-UNIT-payload-0123456789-line-end.")

func suPayloadMeta() content.Metadata {
	sha1Sum := sha1.Sum(suPayload)
	sha256Sum := sha256.Sum256(suPayload)
	md5Sum := md5.Sum(suPayload)
	return content.Metadata{
		SHA1:   hex.EncodeToString(sha1Sum[:]),
		SHA256: hex.EncodeToString(sha256Sum[:]),
		MD5:    hex.EncodeToString(md5Sum[:]),
		Size:   int64(len(suPayload)),
	}
}

// suCounterOpener 记录内容打开，回放固定字节或错误；HEAD 零打开由此断言。
// suCounterOpener records content opens and replays fixed bytes or an
// error; HEAD's zero-open guarantee is asserted through it.
type suCounterOpener struct {
	events *suEvents
	mu     sync.Mutex
	opens  int
	err    error
}

type suReadCloser struct {
	data     []byte
	offset   int
	closed   bool
	closeErr error
}

func (r *suReadCloser) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func (r *suReadCloser) Close() error {
	r.closed = true
	return r.closeErr
}

func (o *suCounterOpener) Open(context.Context, string) (io.ReadCloser, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opens++
	o.events.add("open")
	if o.err != nil {
		return nil, o.err
	}
	return &suReadCloser{data: suPayload}, nil
}

func (o *suCounterOpener) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens
}

// suReporter 并发安全收集固定失败类别。
// suReporter collects fixed failure categories safely under concurrency.
type suReporter struct {
	mu    sync.Mutex
	items []httpserver.DownloadFailure
}

func (r *suReporter) report(c httpserver.DownloadFailure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, c)
}

func (r *suReporter) snapshot() []httpserver.DownloadFailure {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]httpserver.DownloadFailure(nil), r.items...)
}

// suClock 是确定性假时钟：处理器私有 now 被替换为它，阻塞/过期全部由推进
// 时间驱动，绝不做真实睡眠。
// suClock is the deterministic fake clock: the handler's private now is
// replaced with it so blocking/expiry advance by clock motion only, never by
// real sleeps.
type suClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *suClock) current() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *suClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// suEntry 是一次组装：真实 NewAuthenticatedDownload + 测试替身与假时钟。
// suEntry is one composition: the real NewAuthenticatedDownload plus test
// peers and the fake clock.
// suEvents 是跨测试替身的有序阶段记录，nil 表示该测试不观察全序。
// suEvents records a shared total order across test peers; nil means unobserved.
type suEvents struct {
	mu    sync.Mutex
	items []string
}

func (e *suEvents) add(s string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.items = append(e.items, s)
}
func (e *suEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.items...)
}
func (e *suEvents) reset() { e.mu.Lock(); defer e.mu.Unlock(); e.items = nil }

type suEntry struct {
	events  *suEvents
	t       *testing.T
	handler *authenticatedDownload
	backend *suBackend
	store   *suCounterStore
	opener  *suCounterOpener
	report  *suReporter
	clock   *suClock
}

// suDefaultConfig 返回边界内合法配置；调用方可覆写个别字段。
// suDefaultConfig returns an in-bounds configuration; callers may override
// individual fields.
func suDefaultConfig() HTTPConfig {
	return HTTPConfig{
		RepoKey:          suRepoKey,
		Challenge:        suChallenge,
		FailureThreshold: 3,
		FailureWindow:    10 * time.Second,
		BlockDuration:    30 * time.Second,
		MaxTrackedUsers:  64,
	}
}

// suDefaultBackend 返回能通过启动并放行读取的替身后端。
// suDefaultBackend returns a peer backend that passes startup and allows
// reads.
func suDefaultBackend() *suBackend {
	return &suBackend{
		authPrincipal: Principal{id: suUserID, username: suUser},
		repos: map[string]RepositoryInfo{
			suRepoKey:  {Key: suRepoKey, Kind: "local", PackageType: "generic", Revision: 4},
			suOtherKey: {Key: suOtherKey, Kind: "local", PackageType: "generic", Revision: 9},
		},
		canRead: true,
	}
}

// suNew 以替身与假时钟构造真实入口，失败即终止测试。返回的处理器断言为
// *authenticatedDownload 且私有 now 非空，再替换为假时钟。
// suNew composes the real entry with peers and the fake clock, failing the
// test on constructor error. The handler must be an *authenticatedDownload
// with a nonnil private now, which is then replaced by the fake clock.
func suNew(t *testing.T, mutate func(*HTTPConfig), backend Backend) *suEntry {
	t.Helper()
	cfg := suDefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	events := &suEvents{}
	backend.(*suBackend).events = events
	store := &suCounterStore{events: events}
	opener := &suCounterOpener{events: events}
	report := &suReporter{}
	cfg.Report = report.report
	h, err := NewAuthenticatedDownload(context.Background(), cfg, backend, store, opener)
	if err != nil {
		t.Fatalf("NewAuthenticatedDownload error = %v", err)
	}
	entry, ok := h.(*authenticatedDownload)
	if !ok {
		t.Fatalf("handler type = %T, want *authenticatedDownload", h)
	}
	if entry.now == nil {
		t.Fatal("authenticatedDownload.now is nil")
	}
	b := backend.(*suBackend)
	b.mu.Lock()
	b.authCalls, b.lookups, b.readCalls = 0, 0, 0
	b.order = nil
	b.mu.Unlock()
	events.reset()
	clock := &suClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	entry.now = clock.current
	return &suEntry{
		t: t, handler: entry, backend: backend.(*suBackend), events: events,
		store: store, opener: opener, report: report, clock: clock,
	}
}

// suNewDefault 是默认替身的便捷组装。
// suNewDefault is the convenience composition with default peers.
func suNewDefault(t *testing.T) *suEntry {
	t.Helper()
	return suNew(t, nil, suDefaultBackend())
}

// suServer 启动真实 httptest 服务器并注册清理。
// suServer starts a real httptest server and registers cleanup.
func (e *suEntry) suServer() *httptest.Server {
	e.t.Helper()
	srv := httptest.NewServer(e.handler)
	e.t.Cleanup(srv.Close)
	return srv
}

// suGet 对服务器发起真实请求并返回响应。
// suGet issues a real request against the server and returns the response.
func suDo(srv *httptest.Server, method, target, authorization string) (*http.Response, error) {
	req, err := http.NewRequest(method, srv.URL+target, nil)
	if err != nil {
		return nil, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return http.DefaultClient.Do(req)
}

// suBasic 构造标准 Basic 头。
// suBasic builds the standard Basic header.
func suBasic(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// suBodyString 读取并关闭响应正文。
// suBodyString drains and closes a response body.
func suBodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}
	return string(data)
}

// suAssertError 冻结错误表示契约：固定正文、显式 CL、无表示头；401 必须
// 携带 challenge，403/400/404/500 必须没有。
// suAssertError freezes the error representation contract: the fixed body,
// explicit Content-Length, no representation headers; a 401 must carry the
// challenge while 403/400/404/500 must not.
func suAssertError(t *testing.T, resp *http.Response, body string, wantChallenge bool) {
	t.Helper()
	if body != suBodyString(t, resp) {
		t.Fatalf("body = %q, want %q", body, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length = %q, want %d", cl, len(body))
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	if wantChallenge && challenge != suChallenge {
		t.Fatalf("WWW-Authenticate = %q, want %q", challenge, suChallenge)
	}
	if !wantChallenge && challenge != "" {
		t.Fatalf("unexpected WWW-Authenticate = %q", challenge)
	}
	for _, h := range []string{"ETag", "Content-Disposition", "X-Checksum-Sha1", "X-Checksum-Md5", "X-Checksum-Sha256", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			t.Fatalf("error response carries representation header %s=%q", h, v)
		}
	}
}

// suAssertStatus 断言状态码。
// suAssertStatus asserts the status code.
func suAssertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d", resp.StatusCode, want)
	}
}

// ---------------------------------------------------------------------------
// 构造器失败关闭 / Constructor fail-closed
// ---------------------------------------------------------------------------

// TestSecurityNewAuthenticatedDownloadConfigBounds 冻结全部配置边界：依赖
// nil、非法 RepoKey/Challenge、阈值/窗口/时长/容量的开闭区间；边界值本身
// （1/100、1s/1h、1/4096）必须可用。失败一律 (nil, ErrConfiguration)。
// TestSecurityNewAuthenticatedDownloadConfigBounds freezes every config
// bound: nil dependencies, invalid RepoKey/Challenge and the open/closed
// intervals of threshold/window/durations/capacity; the boundary values
// themselves (1/100, 1s/1h, 1/4096) must be accepted. Failures return
// (nil, ErrConfiguration).
func TestSecurityNewAuthenticatedDownloadConfigBounds(t *testing.T) {
	supported := suDefaultBackend()
	mutators := []struct {
		name   string
		mutate func(*HTTPConfig)
	}{
		{"empty repo key", func(c *HTTPConfig) { c.RepoKey = "" }},
		{"dot repo key", func(c *HTTPConfig) { c.RepoKey = "." }},
		{"dotdot repo key", func(c *HTTPConfig) { c.RepoKey = ".." }},
		{"slash repo key", func(c *HTTPConfig) { c.RepoKey = "a/b" }},
		{"space repo key", func(c *HTTPConfig) { c.RepoKey = "a b" }},
		{"unicode repo key", func(c *HTTPConfig) { c.RepoKey = "répo" }},
		{"overlong repo key", func(c *HTTPConfig) { c.RepoKey = strings.Repeat("a", 65) }},
		{"empty challenge", func(c *HTTPConfig) { c.Challenge = "" }},
		{"control char challenge", func(c *HTTPConfig) { c.Challenge = "Basic\rrealm" }},
		{"unicode challenge", func(c *HTTPConfig) { c.Challenge = `Basic realm="密"` }},
		{"threshold zero", func(c *HTTPConfig) { c.FailureThreshold = 0 }},
		{"threshold above 100", func(c *HTTPConfig) { c.FailureThreshold = 101 }},
		{"window below 1s", func(c *HTTPConfig) { c.FailureWindow = 999 * time.Millisecond }},
		{"window above 1h", func(c *HTTPConfig) { c.FailureWindow = time.Hour + time.Nanosecond }},
		{"block below 1s", func(c *HTTPConfig) { c.BlockDuration = time.Second - time.Nanosecond }},
		{"block above 1h", func(c *HTTPConfig) { c.BlockDuration = time.Hour + time.Nanosecond }},
		{"max users zero", func(c *HTTPConfig) { c.MaxTrackedUsers = 0 }},
		{"max users above 4096", func(c *HTTPConfig) { c.MaxTrackedUsers = 4097 }},
	}
	for _, m := range mutators {
		t.Run(m.name, func(t *testing.T) {
			h, err := NewAuthenticatedDownload(context.Background(), withMutated(suDefaultConfig(), m.mutate), supported, &suCounterStore{}, &suCounterOpener{})
			if h != nil || !errors.Is(err, ErrConfiguration) {
				t.Fatalf("result = (%v,%v), want (nil,ErrConfiguration)", h != nil, err)
			}
		})
	}
	valid := []struct {
		name   string
		mutate func(*HTTPConfig)
	}{
		{"threshold 1", func(c *HTTPConfig) { c.FailureThreshold = 1 }},
		{"threshold 100", func(c *HTTPConfig) { c.FailureThreshold = 100 }},
		{"window 1s", func(c *HTTPConfig) { c.FailureWindow = time.Second }},
		{"window 1h", func(c *HTTPConfig) { c.FailureWindow = time.Hour }},
		{"block 1s", func(c *HTTPConfig) { c.BlockDuration = time.Second }},
		{"block 1h", func(c *HTTPConfig) { c.BlockDuration = time.Hour }},
		{"max users 1", func(c *HTTPConfig) { c.MaxTrackedUsers = 1 }},
		{"max users 4096", func(c *HTTPConfig) { c.MaxTrackedUsers = 4096 }},
		{"single char repo key", func(c *HTTPConfig) { c.RepoKey = "a" }},
	}
	for _, m := range valid {
		t.Run("accepts "+m.name, func(t *testing.T) {
			cfg := withMutated(suDefaultConfig(), m.mutate)
			backend := suDefaultBackend()
			backend.repos = map[string]RepositoryInfo{cfg.RepoKey: {Key: cfg.RepoKey, Kind: "local", PackageType: "generic", Revision: 1}}
			h, err := NewAuthenticatedDownload(context.Background(), cfg, backend, &suCounterStore{}, &suCounterOpener{})
			if err != nil || h == nil {
				t.Fatalf("result = (%v,%v), want handler", h != nil, err)
			}
		})
	}
}

// withMutated 应用一次配置变更。
// withMutated applies one configuration mutation.
func withMutated(cfg HTTPConfig, mutate func(*HTTPConfig)) HTTPConfig {
	mutate(&cfg)
	return cfg
}

// TestSecurityNewAuthenticatedDownloadNilDependencies 断言 nil ctx 与三个
// nil 依赖均拒绝构造，替身不被触碰。
// TestSecurityNewAuthenticatedDownloadNilDependencies requires rejection for
// a nil ctx and each nil dependency, with the peer untouched.
func TestSecurityNewAuthenticatedDownloadNilDependencies(t *testing.T) {
	cfg := suDefaultConfig()
	backend := suDefaultBackend()
	store := &suCounterStore{}
	opener := &suCounterOpener{}
	cases := []struct {
		name    string
		ctx     context.Context
		backend Backend
		store   localrepo.FileReadStore
		opener  localrepo.FileOpener
	}{
		{"nil ctx", nil, backend, store, opener},
		{"nil backend", context.Background(), nil, store, opener},
		{"nil nodes store", context.Background(), backend, nil, opener},
		{"nil content opener", context.Background(), backend, store, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := NewAuthenticatedDownload(c.ctx, cfg, c.backend, c.store, c.opener)
			if h != nil || !errors.Is(err, ErrConfiguration) {
				t.Fatalf("result = (%v,%v), want (nil,ErrConfiguration)", h != nil, err)
			}
		})
	}
	if _, _, _, order := backend.snapshot(); len(order) != 0 {
		t.Fatalf("backend touched during rejected constructions: %v", order)
	}
}

// TestSecurityNewAuthenticatedDownloadStartupFailures 冻结启动次序：先
// Validate 再 LookupRepository(RepoKey)；任一失败或仓库缺席/形状不符
// （remote/maven/revision 0）都拒绝构造，不做半配置回退。
// TestSecurityNewAuthenticatedDownloadStartupFailures freezes the startup
// order: Validate precedes LookupRepository(RepoKey); any failure plus
// absence or out-of-shape rows (remote/maven/revision 0) reject construction
// with no half-config fallback.
func TestSecurityNewAuthenticatedDownloadStartupFailures(t *testing.T) {
	valid := suDefaultBackend()
	cases := []struct {
		name    string
		backend *suBackend
	}{
		{"validate error", func() *suBackend { b := suDefaultBackend(); b.validateErr = ErrSecurityStore; return b }()},
		{"catalog store error", func() *suBackend {
			b := suDefaultBackend()
			b.lookupFn = func(string) (RepositoryInfo, error) { return RepositoryInfo{}, ErrSecurityStore }
			return b
		}()},
		{"configured repo absent", func() *suBackend { b := suDefaultBackend(); delete(b.repos, suRepoKey); return b }()},
		{"remote kind", func() *suBackend {
			b := suDefaultBackend()
			b.repos[suRepoKey] = RepositoryInfo{Key: suRepoKey, Kind: "remote", PackageType: "generic", Revision: 4}
			return b
		}()},
		{"maven package type", func() *suBackend {
			b := suDefaultBackend()
			b.repos[suRepoKey] = RepositoryInfo{Key: suRepoKey, Kind: "local", PackageType: "maven", Revision: 4}
			return b
		}()},
		{"revision zero", func() *suBackend {
			b := suDefaultBackend()
			b.repos[suRepoKey] = RepositoryInfo{Key: suRepoKey, Kind: "local", PackageType: "generic", Revision: 0}
			return b
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := NewAuthenticatedDownload(context.Background(), suDefaultConfig(), c.backend, &suCounterStore{}, &suCounterOpener{})
			if h != nil || !errors.Is(err, ErrConfiguration) {
				t.Fatalf("result = (%v,%v), want (nil,ErrConfiguration)", h != nil, err)
			}
		})
	}
	// 成功路径的启动调用次序 / startup order on the accepted path.
	if _, err := NewAuthenticatedDownload(context.Background(), suDefaultConfig(), valid, &suCounterStore{}, &suCounterOpener{}); err != nil {
		t.Fatal(err)
	}
	_, lookups, _, order := valid.snapshot()
	if len(order) != 2 || order[0] != "validate" || order[1] != "catalog" {
		t.Fatalf("startup order = %v, want [validate catalog]", order)
	}
	if lookups != 1 {
		t.Fatalf("startup lookups = %d, want 1", lookups)
	}
}

// ---------------------------------------------------------------------------
// 请求级认证渲染 / Request-level authentication rendering
// ---------------------------------------------------------------------------

// suAuthFailCases 构造各类畸形/未支持 Authorization 值。
// suAuthFailCases builds malformed and unsupported Authorization values.
func suAuthFailCases() []struct {
	name string
	auth string
} {
	huge := "Basic " + base64.StdEncoding.EncodeToString([]byte(suUser+":"+strings.Repeat("p", 8300)))
	return []struct {
		name string
		auth string
	}{
		{"no header", ""},
		{"bad base64", "Basic !!!not-base64!!!"},
		{"no colon", "Basic " + base64.StdEncoding.EncodeToString([]byte("userwithoutcolon"))},
		{"empty username", "Basic " + base64.StdEncoding.EncodeToString([]byte(":pw"))},
		{"empty password", "Basic " + base64.StdEncoding.EncodeToString([]byte(suUser+":"))},
		{"overlong password", "Basic " + base64.StdEncoding.EncodeToString([]byte(suUser+":"+strings.Repeat("p", 1025)))},
		{"bearer token", "Bearer eyJhbGciOiJIUzI1NiJ9.not-a-jwt.sig"},
		{"unsupported scheme", "Digest username=\"x\""},
		{"empty value", " "},
		{"overlong header value", huge},
	}
}

// TestSecurityEntryAuthenticationRejectsMalformedCredentials 断言一切非
// 合法 Basic 单值凭据都得到 401+challenge 的固定表示，且后端/节点/内容
// 完全不被触碰（零越权工作）。小写 basic 同样按方案匹配处理。
// TestSecurityEntryAuthenticationRejectsMalformedCredentials requires the
// fixed 401+challenge representation for every non-conforming single-value
// credential while backend/nodes/content stay untouched (zero privileged
// work). A lowercase basic receives the same scheme handling.
func TestSecurityEntryAuthenticationRejectsMalformedCredentials(t *testing.T) {
	for _, c := range suAuthFailCases() {
		t.Run(c.name, func(t *testing.T) {
			e := suNewDefault(t)
			srv := e.suServer()
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/artifactory/"+suRepoKey+"/x.bin", nil)
			if c.auth != "" {
				if c.auth == " " {
					req.Header.Set("Authorization", "")
				} else {
					req.Header.Set("Authorization", c.auth)
				}
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request error = %v", err)
			}
			suAssertStatus(t, resp, http.StatusUnauthorized)
			suAssertError(t, resp, "unauthorized\n", true)
			authCalls, lookups, readCalls, _ := e.backend.snapshot()
			if authCalls != 0 || lookups != 0 || readCalls != 0 {
				t.Fatalf("backend work on malformed credentials: auth=%d catalog=%d policy=%d", authCalls, lookups, readCalls)
			}
			if n := e.store.count(); n != 0 {
				t.Fatalf("node lookups = %d, want 0", n)
			}
			if n := e.opener.count(); n != 0 {
				t.Fatalf("opens = %d, want 0", n)
			}
		})
	}
}

// TestSecurityEntryAuthenticationSingleHeaderValue 断言 Authorization 只
// 接受恰一个值：重复头拒绝且不触后端。
// TestSecurityEntryAuthenticationSingleHeaderValue accepts exactly one
// Authorization value: duplicates are rejected without backend work.
// TestSecurityEntryAuthenticationLowercaseSchemeAccepted 断言 Basic 方案
// 匹配大小写不敏感：小写 basic 的合法形状进入正常认证路径（错误口令 401
// 并触达后端一次），不是格式拒绝。
// TestSecurityEntryAuthenticationLowercaseSchemeAccepted asserts the Basic
// scheme matches case-insensitively: a lowercase basic with valid shape
// enters the normal authentication path (wrong password 401 with exactly one
// backend call), not a format rejection.
func TestSecurityEntryAuthenticationLowercaseSchemeAccepted(t *testing.T) {
	e := suNewDefault(t)
	srv := e.suServer()
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin",
		"basic "+base64.StdEncoding.EncodeToString([]byte(suUser+":wrong")))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusUnauthorized)
	suAssertError(t, resp, "unauthorized\n", true)
	if authCalls, _, _, _ := e.backend.snapshot(); authCalls != 1 {
		t.Fatalf("backend authenticate calls = %d, want 1", authCalls)
	}
}

func TestSecurityEntryAuthenticationSingleHeaderValue(t *testing.T) {
	e := suNewDefault(t)
	srv := e.suServer()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/artifactory/"+suRepoKey+"/x.bin", nil)
	req.Header.Add("Authorization", suBasic(suUser, suPassword))
	req.Header.Add("Authorization", suBasic(suUser, "other"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusUnauthorized)
	suAssertError(t, resp, "unauthorized\n", true)
	if authCalls, _, _, _ := e.backend.snapshot(); authCalls != 0 {
		t.Fatalf("backend authenticate calls = %d, want 0", authCalls)
	}
}

// TestSecurityEntryAuthenticateErrorMapping 冻结后端 Authenticate 结果到
// HTTP 的映射：拒绝(401+challenge)、禁用(401+challenge 同正文，本地隐私
// 政策)、编码/存储错误(500 无 challenge)、零值/非规范 principal(500)。
// 错误类不进失败计数，因此后续正确凭据仍能到达后端并成功。
// TestSecurityEntryAuthenticateErrorMapping freezes the mapping of backend
// Authenticate results: rejection (401+challenge), disabled (401+challenge
// with the same body — local privacy policy), encoding/store failures (500
// without challenge) and zero/noncanonical principals (500). Error classes
// never enter the failure count, so later valid credentials still reach the
// backend and succeed.
func TestSecurityEntryAuthenticateErrorMapping(t *testing.T) {
	mismatch := Principal{id: suUserID, username: "USER1"}
	cases := []struct {
		name          string
		backend       *suBackend
		wantStatus    int
		wantChallenge bool
	}{
		{"invalid credentials", func() *suBackend { b := suDefaultBackend(); b.authErr = ErrInvalidCredentials; return b }(), http.StatusUnauthorized, true},
		{"account disabled", func() *suBackend { b := suDefaultBackend(); b.authErr = ErrAccountDisabled; return b }(), http.StatusUnauthorized, true},
		{"credential encoding failure", func() *suBackend { b := suDefaultBackend(); b.authErr = ErrCredentialEncoding; return b }(), http.StatusInternalServerError, false},
		{"security store failure", func() *suBackend { b := suDefaultBackend(); b.authErr = ErrSecurityStore; return b }(), http.StatusInternalServerError, false},
		{"zero principal nil error", func() *suBackend { b := suDefaultBackend(); b.zeroPrincipal = true; b.noVerify = true; return b }(), http.StatusInternalServerError, false},
		{"noncanonical principal", func() *suBackend { b := suDefaultBackend(); b.authPrincipal = mismatch; return b }(), http.StatusInternalServerError, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := suNew(t, nil, c.backend)
			srv := e.suServer()
			resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
			if err != nil {
				t.Fatalf("request error = %v", err)
			}
			suAssertStatus(t, resp, c.wantStatus)
			if c.wantStatus == http.StatusUnauthorized {
				suAssertError(t, resp, "unauthorized\n", true)
			} else {
				suAssertError(t, resp, "internal error\n", false)
			}
			if n := e.store.count(); n != 0 {
				t.Fatalf("node lookups = %d, want 0", n)
			}
			if n := e.opener.count(); n != 0 {
				t.Fatalf("opens = %d, want 0", n)
			}
			// 错误类不计数：把后端恢复正常后同用户成功，证明未被阻断。
			// Error classes are not counted: restoring the backend lets the
			// same user succeed, proving no block was armed.
			e.backend.mu.Lock()
			e.backend.authErr = nil
			e.backend.zeroPrincipal = false
			e.backend.noVerify = false
			e.backend.authPrincipal = Principal{id: suUserID, username: suUser}
			e.backend.mu.Unlock()
			resp2, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
			if err != nil {
				t.Fatalf("second request error = %v", err)
			}
			suAssertStatus(t, resp2, http.StatusOK)
			suBodyString(t, resp2)
		})
	}
}

// TestSecurityEntryAuthenticationNormalizesUsername 断言 Basic 用户名按
// 原始解码形传给后端，由真实 Store 做 ASCII 折叠/去界空白（受控子集，不承诺 Unicode 大小写等价），
// 且非 ASCII 用户名直接 401，不触后端。
// TestSecurityEntryAuthenticationNormalizesUsername checks that the Basic
// username reaches the backend in its raw decoded spelling for Store normalization (a controlled subset, no Unicode casefolding claim), while a
// non-ASCII username answers 401 without touching the backend.
func TestSecurityEntryAuthenticationNormalizesUsername(t *testing.T) {
	e := suNewDefault(t)
	srv := e.suServer()
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(" USER1 ", suPassword))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusOK)
	suBodyString(t, resp)
	e.backend.mu.Lock()
	defer e.backend.mu.Unlock()
	if e.backend.lastUser != " USER1 " {
		t.Fatalf("backend username = %q, want %q", e.backend.lastUser, " USER1 ")
	}
	if e.backend.lastPassword != suPassword {
		t.Fatalf("backend password altered: %q", e.backend.lastPassword)
	}
}

func TestSecurityEntryRejectsNonASCIIUsername(t *testing.T) {
	e := suNewDefault(t)
	srv := e.suServer()
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic("用户", suPassword))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusUnauthorized)
	suAssertError(t, resp, "unauthorized\n", true)
	if authCalls, _, _, _ := e.backend.snapshot(); authCalls != 0 {
		t.Fatalf("backend authenticate calls = %d, want 0", authCalls)
	}
}

// TestSecurityEntryExactCallOrderSuccess 冻结成功 GET 的精确全序：
// authenticate → catalog(原始键) → policy → node lookup → open，且 policy
// 收到的正是认证产出的 principal 与目录事实，不混入请求派生角色。
// TestSecurityEntryExactCallOrderSuccess freezes the exact total order of a
// successful GET: authenticate → catalog(raw key) → policy → node lookup →
// open, with policy receiving exactly the authenticated principal and the
// catalog facts, never request-derived roles.
func TestSecurityEntryExactCallOrderSuccess(t *testing.T) {
	e := suNewDefault(t)
	srv := e.suServer()
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/dir/x.bin", suBasic(suUser, suPassword))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusOK)
	if body := suBodyString(t, resp); body != string(suPayload) {
		t.Fatalf("body bytes mismatch: %q", body)
	}
	if got := strings.Join(e.events.snapshot(), ","); got != "authenticate,catalog,policy,node,open" {
		t.Fatalf("stage order=%s", got)
	}
	authCalls, lookups, readCalls, order := e.backend.snapshot()
	if authCalls != 1 || lookups != 1 || readCalls != 1 {
		t.Fatalf("calls = auth %d catalog %d policy %d, want 1/1/1", authCalls, lookups, readCalls)
	}
	want := []string{"authenticate", "catalog", "policy"}
	if len(order) != len(want) {
		t.Fatalf("request order=%v, want %v", order, want)
	}
	got := order
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("request order = %v, want tail %v", order, want)
		}
	}
	e.backend.mu.Lock()
	defer e.backend.mu.Unlock()
	if e.backend.lastKey != suRepoKey {
		t.Fatalf("catalog key = %q, want %q", e.backend.lastKey, suRepoKey)
	}
	if e.backend.lastReader.ID() != suUserID || e.backend.lastReader.Username() != suUser {
		t.Fatalf("policy principal = (%d,%q)", e.backend.lastReader.ID(), e.backend.lastReader.Username())
	}
	if e.backend.lastRepo.Key != suRepoKey || e.backend.lastRepo.Revision != 4 {
		t.Fatalf("policy repo = %+v", e.backend.lastRepo)
	}
	if e.backend.lastPath.Name() != "x.bin" || e.backend.lastPath.Repo() != suRepoKey || !e.backend.lastPath.IsFile() {
		t.Fatalf("policy path = name %q repo %q file %v", e.backend.lastPath.Name(), e.backend.lastPath.Repo(), e.backend.lastPath.IsFile())
	}
}

// TestSecurityEntryPolicyDenialStopsBeforeNodes 冻结 read 拒绝渲染：
// 403 无 challenge、固定正文，存在与缺失路径正文逐字节相同，节点查询为
// 零；policy error 是 500 而非 deny。
// TestSecurityEntryPolicyDenialStopsBeforeNodes freezes the read-denial
// rendering: 403 without challenge, fixed body, byte-identical for existing
// and missing paths, zero node lookups; a policy error is a 500, never a
// silent deny.
func TestSecurityEntryPolicyDenialStopsBeforeNodes(t *testing.T) {
	for _, path := range []string{"/artifactory/" + suRepoKey + "/present.bin", "/artifactory/" + suRepoKey + "/absent.bin"} {
		e := suNew(t, nil, func() *suBackend { b := suDefaultBackend(); b.canRead = false; return b }())
		srv := e.suServer()
		resp, err := suDo(srv, http.MethodGet, path, suBasic(suUser, suPassword))
		if err != nil {
			t.Fatalf("request error = %v", err)
		}
		suAssertStatus(t, resp, http.StatusForbidden)
		suAssertError(t, resp, "forbidden\n", false)
		if n := e.store.count(); n != 0 {
			t.Fatalf("node lookups after deny = %d, want 0", n)
		}
		if n := e.opener.count(); n != 0 {
			t.Fatalf("opens after deny = %d, want 0", n)
		}
	}
	e := suNew(t, nil, func() *suBackend { b := suDefaultBackend(); b.canReadErr = ErrSecurityStore; return b }())
	srv := e.suServer()
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusInternalServerError)
	suAssertError(t, resp, "internal error\n", false)
	if n := e.store.count(); n != 0 {
		t.Fatalf("node lookups after policy error = %d, want 0", n)
	}
}

// TestSecurityEntryCatalogDispatch 冻结目录分发：认证后按原始键查询目录；
// 未知键 404（存在性不由节点推断）、已知受支持但非本处理器配置键 404、
// 已知但不支持形状 500、目录存储错误 500；三者都不触节点。
// TestSecurityEntryCatalogDispatch freezes catalog dispatch: after
// authentication the raw key is resolved through the catalog; an unknown key
// is 404 (existence never inferred from nodes), a known supported
// non-configured key is 404, a known out-of-shape row is 500 and a catalog
// store failure is 500; none reach the nodes.
func TestSecurityEntryCatalogDispatch(t *testing.T) {
	cases := []struct {
		name       string
		backend    *suBackend
		wantStatus int
	}{
		{"unknown repository", func() *suBackend { b := suDefaultBackend(); delete(b.repos, "su.unknown"); return b }(), http.StatusNotFound},
		{"known other supported repository", suDefaultBackend(), http.StatusNotFound},
		{"known remote repository", func() *suBackend {
			b := suDefaultBackend()
			b.repos["su.bad-1"] = RepositoryInfo{Key: "su.bad-1", Kind: "remote", PackageType: "generic", Revision: 1}
			return b
		}(), http.StatusInternalServerError},
		{"catalog store failure", func() *suBackend {
			b := suDefaultBackend()
			b.lookupFn = func(key string) (RepositoryInfo, error) {
				if b.lookups == 1 {
					return b.repos[key], nil
				}
				return RepositoryInfo{}, ErrSecurityStore
			}
			return b
		}(), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := suNew(t, nil, c.backend)
			srv := e.suServer()
			target := "/artifactory/"
			switch c.name {
			case "known other supported repository":
				target += suOtherKey + "/x.bin"
			case "known remote repository":
				target += "su.bad-1/x.bin"
			default:
				target += "su.unknown/x.bin"
			}
			resp, err := suDo(srv, http.MethodGet, target, suBasic(suUser, suPassword))
			if err != nil {
				t.Fatalf("request error = %v", err)
			}
			suAssertStatus(t, resp, c.wantStatus)
			suAssertError(t, resp, suBodyForStatus(c.wantStatus), false)
			if n := e.store.count(); n != 0 {
				t.Fatalf("node lookups = %d, want 0", n)
			}
			if n := e.opener.count(); n != 0 {
				t.Fatalf("opens = %d, want 0", n)
			}
		})
	}
}

// suBodyForStatus 返回 BIN-199 固定正文。
// suBodyForStatus returns the fixed BIN-199 body.
func suBodyForStatus(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "bad request\n"
	case http.StatusUnauthorized:
		return "unauthorized\n"
	case http.StatusForbidden:
		return "forbidden\n"
	case http.StatusNotFound:
		return "not found\n"
	case http.StatusMethodNotAllowed:
		return "method not allowed\n"
	case http.StatusNotImplemented:
		return "not implemented\n"
	default:
		return "internal error\n"
	}
}

// TestSecurityEntrySuccessGetAndHead 冻结成功表示：GET 200 完整字节与全部
// 表示头；HEAD 200 同表示头、零正文且永不打开内容。
// TestSecurityEntrySuccessGetAndHead freezes the success representation:
// GET 200 with the complete bytes and all representation headers; HEAD 200
// with the same headers, a zero body and no content open ever.
func TestSecurityEntrySuccessGetAndHead(t *testing.T) {
	meta := suPayloadMeta()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			e := suNewDefault(t)
			srv := e.suServer()
			resp, err := suDo(srv, method, "/artifactory/"+suRepoKey+"/dir/x.bin", suBasic(suUser, suPassword))
			if err != nil {
				t.Fatalf("request error = %v", err)
			}
			suAssertStatus(t, resp, http.StatusOK)
			body := suBodyString(t, resp)
			if method == http.MethodGet {
				if body != string(suPayload) {
					t.Fatalf("GET body mismatch")
				}
			} else if body != "" {
				t.Fatalf("HEAD body = %q, want empty", body)
			}
			if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(suPayload)) {
				t.Fatalf("Content-Length = %q, want %d", cl, len(suPayload))
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
				t.Fatalf("Content-Type = %q", ct)
			}
			if et := resp.Header.Get("ETag"); et != meta.SHA1 {
				t.Fatalf("ETag = %q, want %q", et, meta.SHA1)
			}
			for h, want := range map[string]string{
				"X-Checksum-Sha1":   meta.SHA1,
				"X-Checksum-Md5":    meta.MD5,
				"X-Checksum-Sha256": meta.SHA256,
			} {
				if got := resp.Header.Get(h); got != want {
					t.Fatalf("%s = %q, want %q", h, got, want)
				}
			}
			if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="x.bin"`) {
				t.Fatalf("Content-Disposition = %q", cd)
			}
			if lm := resp.Header.Get("Last-Modified"); lm == "" {
				t.Fatal("Last-Modified missing")
			}
			if n := e.opener.count(); method == http.MethodHead && n != 0 {
				t.Fatalf("HEAD opens = %d, want 0", n)
			}
		})
	}
}

// TestSecurityEntryNodeFailures 冻结节点/内容失败的既有本地映射：缺节点
// 404（目录存在≠节点存在）、节点存储错误 500、打开失败 500。
// TestSecurityEntryNodeFailures freezes the existing local mapping of
// node/content failures: a missing node is 404 (catalog existence never
// implies node existence), a node store failure is 500 and an open failure
// is 500.
func TestSecurityEntryNodeFailures(t *testing.T) {
	cases := []struct {
		name       string
		store      *suCounterStore
		opener     *suCounterOpener
		wantStatus int
	}{
		{"missing node", &suCounterStore{absent: true}, &suCounterOpener{}, http.StatusNotFound},
		{"node store failure", &suCounterStore{err: errors.New("node lookup exploded")}, &suCounterOpener{}, http.StatusInternalServerError},
		{"open failure", &suCounterStore{}, &suCounterOpener{err: errors.New("open failed")}, http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			backend := suDefaultBackend()
			cfg := suDefaultConfig()
			report := &suReporter{}
			cfg.Report = report.report
			h, err := NewAuthenticatedDownload(context.Background(), cfg, backend, c.store, c.opener)
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			clock := &suClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
			h.(*authenticatedDownload).now = clock.current
			srv := httptest.NewServer(h)
			defer srv.Close()
			resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
			if err != nil {
				t.Fatalf("request error = %v", err)
			}
			suAssertStatus(t, resp, c.wantStatus)
			suAssertError(t, resp, suBodyForStatus(c.wantStatus), false)
		})
	}
}

// TestSecurityEntryMethodRangeAndRawProfile 冻结适配器画像在认证之后仍
// 完整生效：方法 405、Range/If-* 501、raw 画像 400（查询串、点段、
// 百分号段、超长 URI、非小写后缀），且这些已认证请求都不触节点。
// TestSecurityEntryMethodRangeAndRawProfile freezes the adapter profile
// after authentication: method 405, Range/If-* 501 and raw-profile 400
// (query, dot segments, percent segments, overlong URI, non-lowercase
// suffix); none of these authenticated requests touch the nodes.
func TestSecurityEntryMethodRangeAndRawProfile(t *testing.T) {
	type probe struct {
		name       string
		method     string
		uri        string
		header     string
		wantStatus int
	}
	probes := []probe{
		{"POST", http.MethodPost, "/artifactory/" + suRepoKey + "/x.bin", "", http.StatusMethodNotAllowed},
		{"PUT", http.MethodPut, "/artifactory/" + suRepoKey + "/x.bin", "", http.StatusMethodNotAllowed},
		{"range header", http.MethodGet, "/artifactory/" + suRepoKey + "/x.bin", "bytes=0-1", http.StatusNotImplemented},
		{"if-match header", http.MethodGet, "/artifactory/" + suRepoKey + "/x.bin", `W/"abc"`, http.StatusNotImplemented},
		{"query string", http.MethodGet, "/artifactory/" + suRepoKey + "/x.bin?q=1", "", http.StatusBadRequest},
		{"dot segment", http.MethodGet, "/artifactory/" + suRepoKey + "/../x.bin", "", http.StatusBadRequest},
		{"percent segment", http.MethodGet, "/artifactory/" + suRepoKey + "/%41.bin", "", http.StatusBadRequest},
		{"uppercase suffix", http.MethodGet, "/artifactory/" + suRepoKey + "/x.BIN", "", http.StatusBadRequest},
		{"trailing slash", http.MethodGet, "/artifactory/" + suRepoKey + "/dir/", "", http.StatusBadRequest},
		{"overlong URI", http.MethodGet, "/artifactory/" + suRepoKey + "/" + strings.Repeat("a", 8300) + ".bin", "", http.StatusBadRequest},
	}
	for _, p := range probes {
		t.Run(p.name, func(t *testing.T) {
			e := suNewDefault(t)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(p.method, "http://su.test/seed", nil)
			req.RequestURI = p.uri
			req.Header.Set("Authorization", suBasic(suUser, suPassword))
			if p.header != "" {
				switch p.name {
				case "range header":
					req.Header.Set("Range", p.header)
				case "if-match header":
					req.Header.Set("If-Match", p.header)
				}
			}
			e.handler.ServeHTTP(rec, req)
			if rec.Code != p.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, p.wantStatus)
			}
			want := suBodyForStatus(p.wantStatus)
			if p.method == http.MethodHead {
				if rec.Body.Len() != 0 {
					t.Fatalf("HEAD body = %q", rec.Body.String())
				}
			} else if rec.Body.String() != want {
				t.Fatalf("body = %q, want %q", rec.Body.String(), want)
			}
			if n := e.store.count(); n != 0 {
				t.Fatalf("node lookups = %d, want 0", n)
			}
			if n := e.opener.count(); n != 0 {
				t.Fatalf("opens = %d, want 0", n)
			}
		})
	}
	// 未认证的 POST 先被入口拒绝 / an unauthenticated POST is rejected by
	// the entry before any method routing question.
	e := suNewDefault(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://su.test/seed", nil)
	req.RequestURI = "/artifactory/" + suRepoKey + "/x.bin"
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST status = %d, want 401", rec.Code)
	}
}

// TestSecurityEntryCancellationVsDependencyFailure 区分请求取消与依赖
// 失败：请求上下文已取消 → 以 http.ErrAbortHandler 中止且零后端工作；
// 依赖错误而请求仍活跃 → 500 lookup。
// TestSecurityEntryCancellationVsDependencyFailure separates request
// cancellation from dependency failure: an already-cancelled request context
// aborts via http.ErrAbortHandler with zero backend work, while a dependency
// error under an active request answers 500 lookup.
func TestSecurityEntryCancellationVsDependencyFailure(t *testing.T) {
	e := suNewDefault(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://su.test/seed", nil)
	req.RequestURI = "/artifactory/" + suRepoKey + "/x.bin"
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", suBasic(suUser, suPassword))
	aborted := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				if err, ok := r.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
					t.Fatalf("panic = %v, want http.ErrAbortHandler", r)
				}
				aborted = true
			}
		}()
		e.handler.ServeHTTP(rec, req)
	}()
	if !aborted {
		t.Fatal("cancelled request did not abort")
	}
	// 取消保证不触节点；认证是否已被调用由取消检查点位置决定，不作更强
	// 断言（对齐 BIN-199 检查点契约）。
	// Cancellation guarantees zero node work; whether Authenticate already
	// ran depends on the checkpoint placement, so no stronger claim is made
	// (aligned with the BIN-199 checkpoint contract).
	if n := e.store.count(); n != 0 {
		t.Fatalf("node lookups after cancellation = %d, want 0", n)
	}
	if n := e.opener.count(); n != 0 {
		t.Fatalf("opens after cancellation = %d, want 0", n)
	}

	// 依赖自身失败而请求活跃：节点层错误 → 500。
	// A dependency's own failure under an active request: node error → 500.
	store := &suCounterStore{err: errors.New("dependency deadline exceeded")}
	backend := suDefaultBackend()
	report := &suReporter{}
	cfg := suDefaultConfig()
	cfg.Report = report.report
	h, err := NewAuthenticatedDownload(context.Background(), cfg, backend, store, &suCounterOpener{})
	if err != nil {
		t.Fatalf("constructor error = %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusInternalServerError)
	suAssertError(t, resp, "internal error\n", false)
}

// ---------------------------------------------------------------------------
// 确定性失败门 / Deterministic failure gate
// ---------------------------------------------------------------------------

// suWrongRequest 发送一次错误密码请求并断言 401+challenge。
// suWrongRequest sends one wrong-password request asserting 401+challenge.
func suWrongRequest(t *testing.T, srv *httptest.Server, username string) {
	t.Helper()
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(username, "definitely-wrong"))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusUnauthorized)
	suAssertError(t, resp, "unauthorized\n", true)
}

// suRightRequest 发送一次正确凭据请求并断言 200 完整字节。
// suRightRequest sends one valid-credential request asserting 200 bytes.
func suRightRequest(t *testing.T, srv *httptest.Server, username string) {
	t.Helper()
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(username, suPassword))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusOK)
	if body := suBodyString(t, resp); body != string(suPayload) {
		t.Fatalf("body bytes mismatch")
	}
}

// TestSecurityEntryFailureGateBlocksAfterThreshold 冻结阈值语义：N 次错误
// 各得 401，第 N+1 次即使凭据正确也是 403+challenge 且完全不触后端/节点；
// 阻断过期由假时钟推进恢复，成功认证清除计数。
// TestSecurityEntryFailureGateBlocksAfterThreshold freezes threshold
// semantics: N wrong attempts each answer 401 and the N+1st — even with
// valid credentials — is 403+challenge with zero backend/node work; expiry
// recovers via fake-clock motion and a successful authentication clears the
// count.
func TestSecurityEntryFailureGateBlocksAfterThreshold(t *testing.T) {
	e := suNewDefault(t) // threshold 3
	srv := e.suServer()
	for i := 0; i < 3; i++ {
		suWrongRequest(t, srv, suUser)
	}
	before, _, _, _ := e.backend.snapshot()
	if before != 3 {
		t.Fatalf("authenticate calls = %d, want 3", before)
	}
	// 第 4 次：正确凭据也被阻断，403+challenge，零后端工作。
	// Fourth attempt: valid credentials blocked with 403+challenge and no
	// backend work at all.
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusForbidden)
	suAssertError(t, resp, "forbidden\n", true)
	after, _, readCalls, _ := e.backend.snapshot()
	if after != 3 || readCalls != 0 {
		t.Fatalf("blocked request touched backend: auth=%d policy=%d", after, readCalls)
	}
	if n := e.store.count(); n != 0 {
		t.Fatalf("blocked request node lookups = %d", n)
	}
	// 阻断过期：推进假时钟超过 BlockDuration 后正确凭据恢复 200。
	// Block expiry: advancing the fake clock past BlockDuration lets the
	// valid credentials succeed again.
	e.clock.advance(31 * time.Second)
	suRightRequest(t, srv, suUser)
}

// TestSecurityEntryFailureGateSuccessResetsCounters 断言成功认证清除失败
// 计数：两次错误后成功，再两次错误仍不满阈值。
// TestSecurityEntryFailureGateSuccessResetsCounters asserts a successful
// authentication clears failures: two wrongs, one success, two more wrongs
// still below threshold.
func TestSecurityEntryFailureGateSuccessResetsCounters(t *testing.T) {
	e := suNewDefault(t) // threshold 3
	srv := e.suServer()
	suWrongRequest(t, srv, suUser)
	suWrongRequest(t, srv, suUser)
	suRightRequest(t, srv, suUser)
	suWrongRequest(t, srv, suUser)
	suWrongRequest(t, srv, suUser)
	suRightRequest(t, srv, suUser)
}

// TestSecurityEntryFailureGateWindowExpiry 断言窗口过期重置计数：窗口外的
// 旧失败不累计进阈值。
// TestSecurityEntryFailureGateWindowExpiry asserts window expiry resets the
// count: stale failures outside the window never accumulate to threshold.
func TestSecurityEntryFailureGateWindowExpiry(t *testing.T) {
	e := suNewDefault(t) // window 10s, threshold 3
	srv := e.suServer()
	suWrongRequest(t, srv, suUser)
	suWrongRequest(t, srv, suUser)
	e.clock.advance(11 * time.Second)
	suWrongRequest(t, srv, suUser) // 旧的两次已出窗 / the two stale failures left the window
	suRightRequest(t, srv, suUser) // 若窗口未重置则这里已被阻断 / a non-resetting window would block here
}

// TestSecurityEntryFailureGateCountsOnlyInvalidCredentials 断言禁用与后端
// 错误不进计数：3 次禁用/编码错误后正确凭据仍 200。
// TestSecurityEntryFailureGateCountsOnlyInvalidCredentials asserts disabled
// and backend-error classes never count: after three such attempts the valid
// credentials still answer 200.
func TestSecurityEntryFailureGateCountsOnlyInvalidCredentials(t *testing.T) {
	for _, failure := range []error{ErrAccountDisabled, ErrCredentialEncoding, ErrSecurityStore} {
		t.Run(failure.Error(), func(t *testing.T) {
			backend := suDefaultBackend()
			backend.authErr = failure
			e := suNew(t, nil, backend)
			srv := e.suServer()
			status, body, challenge := 500, "internal error\n", false
			if errors.Is(failure, ErrAccountDisabled) {
				status, body, challenge = 401, "unauthorized\n", true
			}
			for i := 0; i < 3; i++ {
				resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
				if err != nil {
					t.Fatal(err)
				}
				suAssertStatus(t, resp, status)
				suAssertError(t, resp, body, challenge)
			}
			e.backend.mu.Lock()
			e.backend.authErr = nil
			e.backend.mu.Unlock()
			suRightRequest(t, srv, suUser)
		})
	}
}

// TestSecurityEntryFailureGateNormalizedUsernameKey 断言失败门以规范用户名
// 为进程内键：大小写/边界空白拼写共享同一计数桶。
// TestSecurityEntryFailureGateNormalizedUsernameKey asserts the gate keys on
// the normalized username: mixed-case and boundary-whitespace spellings
// share one counting bucket.
func TestSecurityEntryFailureGateNormalizedUsernameKey(t *testing.T) {
	e := suNew(t, func(c *HTTPConfig) { c.FailureThreshold = 2 }, suDefaultBackend())
	srv := e.suServer()
	suWrongRequest(t, srv, "USER1")
	suWrongRequest(t, srv, " user1 ")
	// 两种拼写已合计 2 次：任意拼写都阻断。
	// The two spellings total two failures: any spelling now blocks.
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic("User1", suPassword))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusForbidden)
	suAssertError(t, resp, "forbidden\n", true)
}

// TestSecurityEntryFailureGateIsolatedPerUser 断言阻断按用户隔离：A 被阻断
// 不影响 B 的任何阶段（B 仍完整走认证→授权→节点）。
// TestSecurityEntryFailureGateIsolatedPerUser asserts blocking is per user:
// blocking A never impairs any stage of B (full authenticate→policy→node).
func TestSecurityEntryFailureGateIsolatedPerUser(t *testing.T) {
	e := suNewDefault(t)
	srv := e.suServer()
	for i := 0; i < 3; i++ {
		suWrongRequest(t, srv, "blocked.user")
	}
	suRightRequest(t, srv, suUser)
	if n := e.store.count(); n != 1 {
		t.Fatalf("independent user node lookups = %d, want 1", n)
	}
}

// TestSecurityEntryFailureGateCapacityBounded 冻结容量语义：容量被仍在
// 阻断期的身份占满时，新失败无法记录 → 500（不能为绕过阻断而驱逐）；
// 占用者过期释放后恢复记录 401。
// TestSecurityEntryFailureGateCapacityBounded freezes capacity semantics:
// while capacity is held by still-blocked identities a new failure cannot be
// recorded and answers operational 500 (no eviction to bypass blocking);
// after the holder expires the entry is freed and 401 recording resumes.
func TestSecurityEntryFailureGateCapacityBounded(t *testing.T) {
	e := suNew(t, func(c *HTTPConfig) { c.MaxTrackedUsers = 1; c.FailureThreshold = 1 }, suDefaultBackend())
	srv := e.suServer()
	suWrongRequest(t, srv, "holder") // 立即占满并阻断 / fills capacity and blocks at once
	// 新用户的错误密码无法记录 → 500，且未被计入。
	// The new user's wrong password cannot be recorded → 500, uncounted.
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic("victim", "wrong"))
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	suAssertStatus(t, resp, http.StatusInternalServerError)
	suAssertError(t, resp, "internal error\n", false)
	// victim 未被错误计数：占用者过期后正确凭据成功。
	// victim was not negatively counted: after the holder expires the valid
	// credentials succeed.
	e.clock.advance(31 * time.Second)
	suRightRequest(t, srv, "victim")
	// 释放后新的失败重新可记录 / recording works again once freed.
	suWrongRequest(t, srv, "fresh.user")
}

// TestSecurityEntryFailureGateExpiredEntriesPruned 断言过期条目被删除以
// 约束内存：容量 2 下两个旧失败出窗后第三个身份仍可记录（不是 500）。
// TestSecurityEntryFailureGateExpiredEntriesPruned asserts expired entries
// are deleted to bound memory: with capacity 2, after the stale failures
// leave the window a third identity is still recordable (not a 500).
func TestSecurityEntryFailureGateExpiredEntriesPruned(t *testing.T) {
	e := suNew(t, func(c *HTTPConfig) { c.MaxTrackedUsers = 2; c.FailureThreshold = 3 }, suDefaultBackend())
	srv := e.suServer()
	suWrongRequest(t, srv, "stale-1")
	suWrongRequest(t, srv, "stale-2")
	e.clock.advance(11 * time.Second) // 两条均出窗 / both leave the window
	suWrongRequest(t, srv, "fresh-user")
}

// TestSecurityEntryFailureGateConcurrentWrongAttempts 断言并发失败计数无
// 丢失：并发错误密码各自 401/或已触发阻断，收束后同用户必然被阻断且不触
// 后端；同时多用户并发正确请求互不串号。
// TestSecurityEntryFailureGateConcurrentWrongAttempts asserts concurrent
// failure counting loses nothing: concurrent wrong passwords each answer 401
// (or an already-armed block); after they all complete the user is
// definitively blocked with zero backend work. Concurrent valid requests
// across distinct users never mix identities.
func TestSecurityEntryFailureGateConcurrentWrongAttempts(t *testing.T) {
	e := suNew(t, func(c *HTTPConfig) { c.FailureThreshold = 3 }, suDefaultBackend())
	srv := e.suServer()
	const attempts = 8
	var wg sync.WaitGroup
	failures := make(chan string, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, "wrong"))
			if err != nil {
				failures <- err.Error()
				return
			}
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				failures <- readErr.Error()
				return
			}
			if (resp.StatusCode != 401 && resp.StatusCode != 403) || resp.Header.Get("WWW-Authenticate") != suChallenge || string(body) != suBodyForStatus(resp.StatusCode) {
				failures <- fmt.Sprintf("unexpected failure response: status=%d challenge=%q body=%q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"), body)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	beforeAuth, beforeCatalog, beforePolicy, _ := e.backend.snapshot()
	beforeNodes, beforeOpen := e.store.count(), e.opener.count()
	resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
	if err != nil {
		t.Fatal(err)
	}
	suAssertStatus(t, resp, 403)
	suAssertError(t, resp, "forbidden\n", true)
	afterAuth, afterCatalog, afterPolicy, _ := e.backend.snapshot()
	if beforeAuth < 3 || beforeAuth > attempts || afterAuth != beforeAuth || afterCatalog != beforeCatalog || afterPolicy != beforePolicy || e.store.count() != beforeNodes || e.opener.count() != beforeOpen {
		t.Fatalf("blocked request performed extra work: before=%d/%d/%d after=%d/%d/%d", beforeAuth, beforeCatalog, beforePolicy, afterAuth, afterCatalog, afterPolicy)
	}
	if afterCatalog != 0 || afterPolicy != 0 || beforeNodes != 0 || beforeOpen != 0 {
		t.Fatal("failed authentication reached privileged stages")
	}
}

// TestSecurityEntryConcurrentUsersNeverMixed 断言并发多用户正确请求全部
// 200 且授权见到的 principal 与各请求用户一致，无跨用户串号。
// TestSecurityEntryConcurrentUsersNeverMixed asserts all concurrent valid
// requests across distinct users answer 200 while policy observes each
// request's own principal, with no cross-user mixing.
func TestSecurityEntryConcurrentUsersNeverMixed(t *testing.T) {
	const users = 12
	backend := suDefaultBackend()
	backend.authPrincipal = Principal{}
	backend.authFn = func(username, password string) (Principal, error) {
		index, err := strconv.Atoi(strings.TrimPrefix(username, "user"))
		if err != nil || index < 0 || index >= users || password != suPassword {
			return Principal{}, ErrInvalidCredentials
		}
		return Principal{id: int64(100 + index), username: username}, nil
	}
	var observedMu sync.Mutex
	var observed []string
	backend.readFn = func(p Principal, _ RepositoryInfo, path repository.Path) (bool, error) {
		want := strings.TrimSuffix(path.Name(), ".bin")
		index, err := strconv.Atoi(strings.TrimPrefix(want, "user"))
		if err != nil || p.Username() != want || p.ID() != int64(100+index) {
			return false, errors.New("test peer: cross-request principal mismatch")
		}
		observedMu.Lock()
		observed = append(observed, want)
		observedMu.Unlock()
		return true, nil
	}
	e := suNew(t, nil, backend)
	srv := e.suServer()
	var wg sync.WaitGroup
	failures := make(chan string, users)
	for i := 0; i < users; i++ {
		username := fmt.Sprintf("user%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/"+username+".bin", suBasic(username, suPassword))
			if err != nil {
				failures <- username + ": " + err.Error()
				return
			}
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil || resp.StatusCode != 200 || string(body) != string(suPayload) {
				failures <- fmt.Sprintf("%s status=%d read=%v", username, resp.StatusCode, readErr)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	seen := map[string]bool{}
	for _, username := range observed {
		if seen[username] {
			t.Errorf("duplicate policy identity %s", username)
		}
		seen[username] = true
	}
	if len(seen) != users {
		t.Fatalf("policy observed %d distinct identity/path pairs, want %d", len(seen), users)
	}
}

// TestSecurityEntryReportsFailureCategories 冻结诊断类别：认证错误报
// auth，目录错误报 lookup，policy 错误经桥接报 auth，节点错误报 lookup。
// 类别只含固定枚举，不含任何请求内容。
// TestSecurityEntryReportsFailureCategories freezes diagnostic categories:
// authentication failures report auth, catalog failures report lookup,
// policy failures report auth through the bridge and node failures report
// lookup. Categories carry the fixed enum only, never request data.
func TestSecurityEntryReportsFailureCategories(t *testing.T) {
	run := func(name string, backend *suBackend, store *suCounterStore, want httpserver.DownloadFailure) {
		t.Run(name, func(t *testing.T) {
			report := &suReporter{}
			cfg := suDefaultConfig()
			cfg.Report = report.report
			h, err := NewAuthenticatedDownload(context.Background(), cfg, backend, store, &suCounterOpener{})
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			h.(*authenticatedDownload).now = (&suClock{now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}).current
			srv := httptest.NewServer(h)
			defer srv.Close()
			resp, err := suDo(srv, http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
			if err != nil {
				t.Fatalf("request error = %v", err)
			}
			resp.Body.Close()
			cats := report.snapshot()
			if len(cats) == 0 {
				t.Fatal("no failure category reported")
			}
			if cats[len(cats)-1] != want {
				t.Fatalf("category = %q, want %q", cats[len(cats)-1], want)
			}
		})
	}
	run("authenticate store failure", func() *suBackend { b := suDefaultBackend(); b.authErr = ErrSecurityStore; return b }(), &suCounterStore{}, httpserver.DownloadFailureAuth)
	run("catalog store failure", func() *suBackend {
		b := suDefaultBackend()
		b.lookupFn = func(key string) (RepositoryInfo, error) {
			if b.lookups == 1 {
				return b.repos[key], nil
			}
			return RepositoryInfo{}, ErrSecurityStore
		}
		return b
	}(), &suCounterStore{}, httpserver.DownloadFailureLookup)
	run("policy store failure", func() *suBackend { b := suDefaultBackend(); b.canReadErr = ErrSecurityStore; return b }(), &suCounterStore{}, httpserver.DownloadFailureAuth)
	run("node store failure", suDefaultBackend(), &suCounterStore{err: errors.New("node failure")}, httpserver.DownloadFailureLookup)
}

// 失败随机源不能生成凭据；错误不回显原始输入。
// Failed entropy cannot produce a credential and never echoes the raw input.
func TestSecurityGenerateCredentialEntropyFailure(t *testing.T) {
	const raw = "Synthetic-entropy-failure-password"
	encoded, err := generateCredential(strings.NewReader(""), raw)
	if encoded != "" || !errors.Is(err, ErrConfiguration) || strings.Contains(err.Error(), raw) {
		t.Fatalf("entropy failure result: nonempty=%v err=%v", encoded != "", err)
	}
}

// HEAD 的各类拒绝保留 GET 错误长度，正文严格为空。
// HEAD denial classes retain the GET error length with a strictly empty body.
func TestSecurityEntryHeadErrorRepresentations(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		challenge bool
		auth      bool
		setup     func(*suEntry)
	}{
		{"missing credentials", 401, true, false, nil},
		{"policy denied", 403, false, true, func(e *suEntry) { e.backend.canRead = false }},
		{"missing node", 404, false, true, func(e *suEntry) { e.store.absent = true }},
		{"authentication dependency", 500, false, true, func(e *suEntry) { e.backend.authErr = ErrSecurityStore }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := suNewDefault(t)
			if c.setup != nil {
				c.setup(e)
			}
			srv := e.suServer()
			authorization := ""
			if c.auth {
				authorization = suBasic(suUser, suPassword)
			}
			resp, err := suDo(srv, http.MethodHead, "/artifactory/"+suRepoKey+"/x.bin", authorization)
			if err != nil {
				t.Fatal(err)
			}
			suAssertStatus(t, resp, c.status)
			if body := suBodyString(t, resp); body != "" {
				t.Fatalf("HEAD error body=%q", body)
			}
			if got, want := resp.Header.Get("Content-Length"), strconv.Itoa(len(suBodyForStatus(c.status))); got != want {
				t.Fatalf("HEAD error length=%q want %q", got, want)
			}
			challenge := resp.Header.Get("WWW-Authenticate")
			if c.challenge && challenge != suChallenge || !c.challenge && challenge != "" {
				t.Fatalf("HEAD error challenge=%q", challenge)
			}
			for _, key := range []string{"ETag", "Last-Modified", "Content-Disposition", "X-Checksum-Sha1", "X-Checksum-Sha256", "X-Checksum-Md5"} {
				if resp.Header.Get(key) != "" {
					t.Fatalf("HEAD error leaked representation header %s", key)
				}
			}
			if e.opener.count() != 0 {
				t.Fatal("HEAD error opened content")
			}
		})
	}
	t.Run("temporary block", func(t *testing.T) {
		e := suNew(t, func(c *HTTPConfig) { c.FailureThreshold = 1 }, suDefaultBackend())
		srv := e.suServer()
		suWrongRequest(t, srv, suUser)
		beforeA, beforeC, beforeP, _ := e.backend.snapshot()
		resp, err := suDo(srv, http.MethodHead, "/artifactory/"+suRepoKey+"/x.bin", suBasic(suUser, suPassword))
		if err != nil {
			t.Fatal(err)
		}
		suAssertStatus(t, resp, 403)
		if body := suBodyString(t, resp); body != "" {
			t.Fatalf("HEAD blocked body=%q", body)
		}
		if resp.Header.Get("WWW-Authenticate") != suChallenge || resp.Header.Get("Content-Length") != strconv.Itoa(len("forbidden\n")) {
			t.Fatal("HEAD blocked representation mismatch")
		}
		afterA, afterC, afterP, _ := e.backend.snapshot()
		if beforeA != afterA || beforeC != afterC || beforeP != afterP || e.store.count() != 0 || e.opener.count() != 0 {
			t.Fatal("HEAD blocked request performed backend/node work")
		}
	})
}

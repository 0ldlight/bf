package repository

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// FileMimeCaseProfile 显式选择 ASCII 查询画像，不读取进程 Locale。
// FileMimeCaseProfile selects an ASCII query profile without reading Locale.
type FileMimeCaseProfile uint8

const (
	FileMimeCaseRootASCII FileMimeCaseProfile = iota + 1
	FileMimeCaseTurkicASCII
)

// FileMimeDefinition 保存已解码、非 null 的配置属性；Type 空串合法。
// FileMimeDefinition holds decoded, nonnull attributes; an empty Type is valid.
// Extensions 是逗号分隔原串，配置键的大小写保持不变。
// Extensions is the comma-separated raw attribute; key case stays unchanged.
type FileMimeDefinition struct {
	Type       string
	Extensions string
}

// ErrInvalidFileMimeCatalog 表示无效画像、畸形 UTF-8、重复 Type 或零目录。
// ErrInvalidFileMimeCatalog marks an invalid profile, malformed UTF-8,
// duplicate Type or zero catalog. Its fixed message contains no input.
var ErrInvalidFileMimeCatalog = errors.New("repository: invalid file mime catalog")

// ErrUnsupportedFileMimeExtension 表示原始扩展名含非 ASCII 码元。
// ErrUnsupportedFileMimeExtension marks a non-ASCII original extension.
var ErrUnsupportedFileMimeExtension = errors.New("repository: unsupported file mime extension")

// ErrAmbiguousFileMimeExtension 表示配置键指向不同 Type；不猜覆盖赢家。
// ErrAmbiguousFileMimeExtension marks a key assigned distinct Types;
// no overwrite winner is selected. These are local support limits.
var ErrAmbiguousFileMimeExtension = errors.New("repository: ambiguous file mime extension")

type fileMimeEntry struct {
	typeName  string
	ambiguous bool
}

// FileMimeCatalog 是私有不可变缓存；构造后可复制值并并发只读。
// FileMimeCatalog is an immutable private cache, safe to copy and read
// concurrently after construction. Its zero value is invalid.
type FileMimeCatalog struct {
	cache   map[string]fileMimeEntry
	profile FileMimeCaseProfile
	valid   bool
}

// NewFileMimeCatalog 从属性事实生成目录：Java trim/blank 后保留键大小写，
// 拒绝重复 Type，永久标记跨 Type 冲突；有效画像下空 defs 得有效空目录。
// NewFileMimeCatalog builds the provider from attribute facts, preserving
// key case after Java trim/blank filtering. Duplicate Types are rejected;
// conflicting keys stay ambiguous. Empty definitions form a valid catalog.
// It does not parse XML, choose deployment configuration or read Locale.
func NewFileMimeCatalog(defs []FileMimeDefinition, profile FileMimeCaseProfile) (FileMimeCatalog, error) {
	if profile != FileMimeCaseRootASCII && profile != FileMimeCaseTurkicASCII {
		return FileMimeCatalog{}, ErrInvalidFileMimeCatalog
	}
	cache := make(map[string]fileMimeEntry)
	types := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		if !utf8.ValidString(def.Type) || !utf8.ValidString(def.Extensions) {
			return FileMimeCatalog{}, ErrInvalidFileMimeCatalog
		}
		if _, exists := types[def.Type]; exists {
			return FileMimeCatalog{}, ErrInvalidFileMimeCatalog
		}
		types[def.Type] = struct{}{}
		for _, raw := range strings.Split(def.Extensions, ",") {
			key := javaTrim(raw)
			if javaBlank(key) {
				continue
			}
			entry, exists := cache[key]
			if entry.ambiguous {
				continue
			}
			if exists && entry.typeName != def.Type {
				cache[key] = fileMimeEntry{ambiguous: true}
			} else {
				cache[key] = fileMimeEntry{typeName: def.Type}
			}
		}
	}
	return FileMimeCatalog{cache: cache, profile: profile, valid: true}, nil
}

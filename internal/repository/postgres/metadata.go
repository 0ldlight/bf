// 七列文件元数据的私有 SQL 编解码与绑定边界：本文件只做表示转换，不做
// 长度/十六进制/trim/大小写校验——original 的长度、尾部空格与 NUL 全部交由
// PostgreSQL VARCHAR(1024) 与原生错误决定；actor 复用已批准的 NewActor
// 64 码元缩写。值快照契约见 docs/FILE-METADATA.md，SQL 持久化契约见
// docs/FILE-METADATA-PERSISTENCE.md。
// Private SQL codec and binding boundary for the seven file-metadata
// columns: representation conversion only, with no length/hex/trim/case
// validation — PostgreSQL's VARCHAR(1024) and native errors decide original
// length, trailing spaces and NUL, and actors reuse the approved NewActor
// 64-unit abbreviation. The value-snapshot contract lives in
// docs/FILE-METADATA.md; the SQL persistence contract in
// docs/FILE-METADATA-PERSISTENCE.md.
package postgres

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/0ldlight/bf/internal/repository"
)

// encodeMetadataUnits 把可空原始 UTF-16 original/actor 码元编码为可空 SQL
// 字符串参数：nil 保持 nil（SQL NULL），非 nil（含空）编码为 UTF-8 字符串。
// 有效代理对正常编码；每个不成对代理码元变为 ASCII '?'（0x3f，对齐所选
// PostgreSQL 参数编码边界的观察——标准库 utf16.Decode 的 U+FFFD 行为不是本
// 边界的契约），字面 U+FFFD 保持自身；nil 与空串不同。NUL 码元逐字进入
// 字符串交由数据库裁决，不截断/不替换。无 Go 侧长度或语法校验。
// encodeMetadataUnits encodes nullable raw UTF-16 original/actor units into
// a nullable SQL string parameter: nil stays nil (SQL NULL) and nonnil
// (including empty) becomes a UTF-8 string. Valid surrogate pairs encode
// normally; every unpaired surrogate unit becomes ASCII '?' (0x3f, matching
// the observed selected PostgreSQL parameter-encoding boundary — the
// standard-library utf16.Decode U+FFFD behavior is NOT this boundary's
// contract) while a literal U+FFFD stays itself; nil differs from the empty
// string. NUL units enter the string verbatim for the database to judge —
// never truncated or replaced. No Go-side length or grammar validation
// happens here.
func encodeMetadataUnits(units []uint16) any {
	if units == nil {
		return nil
	}
	var b strings.Builder
	b.Grow(len(units))
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u < 0xD800 || u >= 0xE000:
			// 非代理码元逐字编码（含 NUL 与字面 U+FFFD）。
			// A nonsurrogate unit encodes verbatim (NUL and a literal
			// U+FFFD included).
			b.WriteRune(rune(u))
		case u < 0xDC00 && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] < 0xE000:
			// 高位代理紧跟低位代理：解码为星面字符的正常 UTF-8。
			// A high surrogate followed by a low surrogate decodes to
			// the astral character's normal UTF-8.
			b.WriteRune((rune(u-0xD800)<<10 | rune(units[i+1]-0xDC00)) + 0x10000)
			i++
		default:
			// 不成对代理码元（含孤立低位与反序对）→ ASCII '?'。
			// An unpaired surrogate unit (a lone low surrogate or a
			// reversed pair member) becomes ASCII '?'.
			b.WriteByte('?')
		}
	}
	return b.String()
}

// decodeStoredUnits 把行内 SQL 字符串解回 UTF-16 码元：NULL 返回 nil，空串
// 返回非 nil 零长度（保持 nil != 空），有效 UTF-8 按 rune 重建（星面字符成
// 为代理对）。仅面向数据库返回的有效 UTF-8。
// decodeStoredUnits decodes a stored SQL string back to UTF-16 units: NULL
// yields nil and the empty string a nonnil zero length (preserving nil !=
// empty); valid UTF-8 rebuilds rune by rune (astral characters become
// surrogate pairs). It targets the valid UTF-8 the database returns.
func decodeStoredUnits(ns sql.NullString) []uint16 {
	if !ns.Valid {
		return nil
	}
	units := make([]uint16, 0, len(ns.String))
	for _, r := range ns.String {
		if r > 0xFFFF {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
			continue
		}
		units = append(units, uint16(r))
	}
	return units
}

// decodeStoredActor 把行内 actor 列解回 Actor：NULL 为 null Actor；非 NULL
// 经 NewActor 规范化——超过 64 个 UTF-16 码元的存量值在读取侧按首 61 码元
// 加三个 0x002e 缩写。
// decodeStoredActor decodes a stored actor column back to an Actor: NULL is
// the null Actor; non-NULL values pass through NewActor, so stored values
// above 64 UTF-16 units abbreviate read-side to the first 61 units plus
// three 0x002e units.
func decodeStoredActor(ns sql.NullString) repository.Actor {
	return repository.NewActor(decodeStoredUnits(ns))
}

// metadataArgs 先按值复制快照（调用方不得在操作进行中同时改写其指针内
// 容），再返回七个驱动参数：created/modified/updated 为显式 int64（updated
// 写侧永不 NULL——值模型无 null updated），两个 actor 与两个 original 为
// 可空字符串参数。nil 快照返回七个 nil（SQL NULL）。
// metadataArgs copies the snapshot by value first (callers must not mutate
// the pointed-to snapshot concurrently with an operation) and returns the
// seven driver arguments: created/modified/updated as explicit int64 (the
// write side never has a NULL updated — the value model has no null
// updated) and the two actors plus two originals as nullable string
// parameters. A nil snapshot yields seven nils (SQL NULL).
func metadataArgs(m *repository.FileMetadata) []any {
	if m == nil {
		return []any{nil, nil, nil, nil, nil, nil, nil}
	}
	snap := *m
	return []any{
		snap.CreatedMillis(),
		snap.ModifiedMillis(),
		snap.UpdatedMillis(),
		encodeMetadataUnits(snap.CreatedBy().UTF16()),
		encodeMetadataUnits(snap.ModifiedBy().UTF16()),
		encodeMetadataUnits(snap.SHA1OriginalUTF16()),
		encodeMetadataUnits(snap.MD5OriginalUTF16()),
	}
}

// storedMetadata 是 Lookup 追加的七个元数据列的可空扫描形状。合法形状只有
// 两种：七列全 NULL（历史/未记录，快照为 nil），或 created 与 modified 都
// 非 NULL（updated 可 NULL，读侧映射 0，对齐所选目标读取器）；其余部分
// 形状是本地防御性拒绝。
// storedMetadata is the nullable scan shape of the seven metadata columns
// appended by Lookup. Only two shapes are valid: all seven NULL (legacy or
// never recorded — a nil snapshot) or created and modified both non-NULL
// (updated may be NULL, mapping read-side to 0 like the selected target
// reader); every other partial shape is a defensive local rejection.
type storedMetadata struct {
	created      sql.NullInt64
	modified     sql.NullInt64
	updated      sql.NullInt64
	createdBy    sql.NullString
	modifiedBy   sql.NullString
	sha1Original sql.NullString
	md5Original  sql.NullString
}

// snapshot 把扫描形状转换为独立快照：全 NULL 返回 (nil, nil)；created 与
// modified 已知时构造全新 FileMetadata（updated NULL 映射 0，actor 经
// NewActor，original 恢复可空 UTF-16）；其他部分形状返回有界错误文案，
// Unwrap 链保留 ErrInvalidReference，永不回显行值。
// snapshot converts the scan shape into an independent snapshot: all NULL
// returns (nil, nil); with created and modified known it builds a fresh
// FileMetadata (a NULL updated maps to 0, actors pass through NewActor,
// originals rebuild as nullable UTF-16); any other partial shape returns a
// bounded error preserving ErrInvalidReference under Unwrap and never
// echoing row values.
func (sm storedMetadata) snapshot() (*repository.FileMetadata, error) {
	if !sm.created.Valid && !sm.modified.Valid && !sm.updated.Valid &&
		!sm.createdBy.Valid && !sm.modifiedBy.Valid &&
		!sm.sha1Original.Valid && !sm.md5Original.Valid {
		return nil, nil
	}
	if !sm.created.Valid || !sm.modified.Valid {
		return nil, fmt.Errorf("postgres nodes: stored metadata shape: %w", ErrInvalidReference)
	}
	updated := int64(0)
	if sm.updated.Valid {
		updated = sm.updated.Int64
	}
	snap := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  sm.created.Int64,
		ModifiedMillis: sm.modified.Int64,
		UpdatedMillis:  updated,
		CreatedBy:      decodeStoredActor(sm.createdBy),
		ModifiedBy:     decodeStoredActor(sm.modifiedBy),
		SHA1Original:   decodeStoredUnits(sm.sha1Original),
		MD5Original:    decodeStoredUnits(sm.md5Original),
	})
	return &snap, nil
}

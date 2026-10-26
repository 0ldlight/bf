package repository

import "unicode/utf16"

// MaterializeFileMime 返回 getter 完成缓存后的不可变状态，仅替换 MIME。
// MaterializeFileMime returns immutable getter-completed state, changing
// only MIME. The target getter mutates its source; this API returns a value.
// 缓存值与无点路径旁路目录；有点时先验目录，再验 ASCII 原扩展名。
// Cached values and dotless paths bypass the catalog. Dotted paths check
// the catalog before the original extension's ASCII profile. Failures return
// a zero snapshot and a fixed error. No Original/identity/time normalization,
// XML/Home lookup, copy baseline or save operation is performed.
func MaterializeFileMime(snapshot FileInfoSnapshot, catalog FileMimeCatalog) (FileInfoSnapshot, error) {
	if !snapshot.valid {
		return FileInfoSnapshot{}, ErrInvalidFileInfoSnapshot
	}
	if snapshot.mime != nil {
		return snapshot, nil
	}
	dot := -1
	for i := len(snapshot.repoPath) - 1; i >= 0; i-- {
		if snapshot.repoPath[i] == '.' {
			dot = i
			break
		}
	}
	mime := "application/octet-stream"
	if dot >= 0 {
		if !catalog.valid {
			return FileInfoSnapshot{}, ErrInvalidFileMimeCatalog
		}
		extension := snapshot.repoPath[dot+1:]
		key := make([]rune, len(extension))
		for i, unit := range extension {
			if unit > 0x7f {
				return FileInfoSnapshot{}, ErrUnsupportedFileMimeExtension
			}
			r := rune(unit)
			if r == 'I' && catalog.profile == FileMimeCaseTurkicASCII {
				r = '\u0131'
			} else if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			key[i] = r
		}
		if entry, exists := catalog.cache[string(key)]; exists {
			if entry.ambiguous {
				return FileInfoSnapshot{}, ErrAmbiguousFileMimeExtension
			}
			mime = entry.typeName
		}
	}
	fields := snapshot.Fields()
	fields.MimeTypeUTF16 = utf16.Encode([]rune(mime))
	return NewFileInfoSnapshot(fields)
}

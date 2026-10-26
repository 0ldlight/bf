package repository

import (
	"unicode/utf16"
	"unicode/utf8"
)

// LoadedFileIdentity 将 file Path 转成即时加载文件的仓库、路径和缓存名字。
// 它只做一次路径 trim 与边界斜杠移除，不重建 Path 或执行 force-file。
// 成功返回三个独立的 UTF-16 切片（空串为非 nil 空切片）；失败返回三个
// nil 与不含输入的 ErrInvalidPath。它不构造完整加载快照。
// LoadedFileIdentity converts a file Path into the repo, path and cached name
// of an immediately loaded file. It trims the path and boundary slashes once,
// without reconstructing Path or applying force-file. Success returns three
// independent UTF-16 slices, with nonnil empty slices for empty strings;
// failure returns three nil slices and the input-free ErrInvalidPath. It does
// not construct a complete loaded snapshot.
func LoadedFileIdentity(path Path) (repoKey, repoPath, name []uint16, err error) {
	if !path.IsFile() || !utf8.ValidString(path.Repo()) ||
		!utf8.ValidString(path.Parent()) || !utf8.ValidString(path.Name()) {
		return nil, nil, nil, ErrInvalidPath
	}
	key := javaTrim(path.Repo())
	if key == "" {
		return nil, nil, nil, ErrInvalidPath
	}
	rawPath := trimSlashes(javaTrim(path.PathName()))
	cachedName, ok := repoPathGetName(rawPath)
	if !ok {
		return nil, nil, nil, ErrInvalidPath
	}
	return utf16.Encode([]rune(key)), utf16.Encode([]rune(rawPath)),
		utf16.Encode([]rune(cachedName)), nil
}

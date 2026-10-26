package repository

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"unicode/utf16"
)

func mimeUnits(s string) []uint16 {
	if s == "" {
		return []uint16{}
	}
	return utf16.Encode([]rune(s))
}

func mimeSnapshot(t *testing.T, path, mime []uint16) FileInfoSnapshot {
	t.Helper()
	s, err := NewFileInfoSnapshot(FileInfoSnapshotInput{
		RepoKeyUTF16: mimeUnits(" repo "), RepoPathUTF16: path, NameUTF16: mimeUnits("unrelated.cached.name"),
		CreatedMillis: -11, LastModifiedMillis: 0, LastUpdatedMillis: -22, SizeBytes: -1,
		CreatedByUTF16: []uint16{0xd800}, ModifiedByUTF16: []uint16{}, MimeTypeUTF16: mime,
		Checksums: []ChecksumEntryInput{
			{Kind: ChecksumSHA1, Actual: nil, Original: mimeUnits("IIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIII")},
			{Kind: ChecksumMD5, Actual: []uint16{}, Original: []uint16{0xdc00}},
			{Kind: ChecksumSHA256, Actual: mimeUnits("RAW"), Original: nil},
		},
	})
	if err != nil {
		t.Fatalf("snapshot fixture: %v", err)
	}
	return s
}

func mimeCatalog(t *testing.T, defs []FileMimeDefinition, profile FileMimeCaseProfile) FileMimeCatalog {
	t.Helper()
	c, err := NewFileMimeCatalog(defs, profile)
	if err != nil {
		t.Fatalf("catalog fixture: %v", err)
	}
	return c
}

func mimeWant(t *testing.T, src FileInfoSnapshot, cat FileMimeCatalog, wantMime []uint16) FileInfoSnapshot {
	t.Helper()
	before := src.Fields()
	want := src.Fields()
	want.MimeTypeUTF16 = wantMime
	got, err := MaterializeFileMime(src, cat)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !reflect.DeepEqual(got.Fields(), want) {
		t.Fatalf("MIME or unrelated fields changed: got %#v, want %#v", got.Fields(), want)
	}
	if !reflect.DeepEqual(src.Fields(), before) {
		t.Fatal("source snapshot changed")
	}
	return got
}

func mimeError(t *testing.T, src FileInfoSnapshot, cat FileMimeCatalog, want error) {
	t.Helper()
	before := src.Fields()
	got, err := MaterializeFileMime(src, cat)
	if !errors.Is(err, want) || err.Error() != want.Error() {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if !reflect.DeepEqual(got.Fields(), (FileInfoSnapshot{}).Fields()) {
		t.Fatal("error returned nonzero snapshot fields")
	}
	if _, err := got.IsIdentical(got, false); !errors.Is(err, ErrInvalidFileInfoSnapshot) {
		t.Fatal("error result must be invalid zero snapshot")
	}
	if !reflect.DeepEqual(src.Fields(), before) {
		t.Fatal("failure changed source snapshot")
	}
}

func TestMaterializeFileMimeErrorOrder(t *testing.T) {
	var zero FileMimeCatalog
	valid := mimeCatalog(t, nil, FileMimeCaseRootASCII)
	for _, cat := range []FileMimeCatalog{zero, valid} {
		mimeError(t, FileInfoSnapshot{}, cat, ErrInvalidFileInfoSnapshot)
	}
	for _, path := range [][]uint16{mimeUnits("a.txt"), mimeUnits("a."), mimeUnits("a.é"), {'a', '.', 0xd800}} {
		mimeError(t, mimeSnapshot(t, path, nil), zero, ErrInvalidFileMimeCatalog)
	}
	for _, path := range [][]uint16{mimeUnits("a.é"), {'a', '.', 0xd800}} {
		mimeError(t, mimeSnapshot(t, path, nil), valid, ErrUnsupportedFileMimeExtension)
	}
	if ErrInvalidFileMimeCatalog.Error() != "repository: invalid file mime catalog" ||
		ErrUnsupportedFileMimeExtension.Error() != "repository: unsupported file mime extension" ||
		ErrAmbiguousFileMimeExtension.Error() != "repository: ambiguous file mime extension" {
		t.Fatal("public error messages changed")
	}
}

func TestMaterializeFileMimeCachedAndNoDotBypasses(t *testing.T) {
	var zero FileMimeCatalog
	for _, mime := range [][]uint16{mimeUnits("custom/type"), {}, mimeUnits(" \t"), {0xd800, 0, 0xdc00}} {
		mimeWant(t, mimeSnapshot(t, mimeUnits("a.é"), mime), zero, mime)
	}
	for _, path := range [][]uint16{mimeUnits("README"), mimeUnits("dir/name"), {}, {0xd800, 0, '/', 'a'}} {
		mimeWant(t, mimeSnapshot(t, path, nil), zero, mimeUnits("application/octet-stream"))
	}
}

func TestMaterializeFileMimeWholePathLiteralExpectations(t *testing.T) {
	// Literal native-table expectations: TXT -> text/plain, gz -> gzip, XML
	// -> XML, swift preserves trailing space. Custom opaque keys distinguish
	// whole-path extraction from basename, metadata stripping and trimming.
	cat := mimeCatalog(t, []FileMimeDefinition{
		{Type: "text/plain", Extensions: "txt"}, {Type: "application/x-gzip", Extensions: "gz"},
		{Type: "application/x-java-pack200", Extensions: "jar.pack.gz"},
		{Type: "application/xml", Extensions: "xml"}, {Type: "text/x-swift ", Extensions: "swift"},
		{Type: "parent-dot", Extensions: "dot/name"}, {Type: "metadata-key", Extensions: "xml:properties"},
		{Type: "literal-space", Extensions: "txt x"}, {Type: "nul-key", Extensions: "t\x00xt"},
	}, FileMimeCaseRootASCII)
	for _, tc := range []struct{ path, want string }{
		{"a.TXT", "text/plain"}, {"a.jar.pack.gz", "application/x-gzip"},
		{"a.XML", "application/xml"}, {"a.swift", "text/x-swift "},
		{"dir.with.dot/name", "parent-dot"}, {"dir.with.dot/name.txt", "text/plain"},
		{"a.", "application/octet-stream"}, {"a.unknown", "application/octet-stream"},
		{"a.xml:properties", "metadata-key"}, {"a.xml:statistics", "application/octet-stream"},
		{"a.txt?query", "application/octet-stream"}, {"a.txt x", "literal-space"},
		{"a.txt ", "application/octet-stream"}, {"a.t\x00xt", "nul-key"},
		{"é/文件.txt", "text/plain"},
	} {
		t.Run(tc.path, func(t *testing.T) { mimeWant(t, mimeSnapshot(t, mimeUnits(tc.path), nil), cat, mimeUnits(tc.want)) })
	}
	mimeWant(t, mimeSnapshot(t, []uint16{0xd800, '/', 'a', '.', 't', 'x', 't'}, nil), cat, mimeUnits("text/plain"))
}

func TestMaterializeFileMimeOnlyMimeAndOwnership(t *testing.T) {
	cat := mimeCatalog(t, []FileMimeDefinition{{Type: "text/plain", Extensions: "txt"}}, FileMimeCaseRootASCII)
	path := mimeUnits(" /foreign:properties/FILE.TXT")
	src := mimeSnapshot(t, path, nil)
	path[0] = 'x'
	before := src.Fields()
	out := mimeWant(t, src, cat, mimeUnits("text/plain"))
	fields := out.Fields()
	fields.MimeTypeUTF16[0] = 'X'
	fields.RepoKeyUTF16[0] = 'X'
	fields.RepoPathUTF16[0] = 'X'
	fields.NameUTF16[0] = 'X'
	fields.CreatedByUTF16[0] = 'X'
	fields.Checksums[0].Original[0] = 'X'
	fields.Checksums[2].Actual[0] = 'X'
	mimeWant(t, src, cat, mimeUnits("text/plain"))
	want := src.Fields()
	want.MimeTypeUTF16 = mimeUnits("text/plain")
	if !reflect.DeepEqual(out.Fields(), want) || !reflect.DeepEqual(src.Fields(), before) {
		t.Fatal("returned fields leaked writable ownership")
	}
	// Reusing cached output with no catalog preserves its exact payload.
	mimeWant(t, out, FileMimeCatalog{}, mimeUnits("text/plain"))
}

func TestMaterializeFileMimeConcurrentReadOwnership(t *testing.T) {
	cat := mimeCatalog(t, []FileMimeDefinition{{Type: "text/plain", Extensions: "txt"}}, FileMimeCaseRootASCII)
	src := mimeSnapshot(t, mimeUnits("a.TXT"), nil)
	want := src.Fields()
	want.MimeTypeUTF16 = mimeUnits("text/plain")
	var wg sync.WaitGroup
	failures := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(c FileMimeCatalog) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				out, err := MaterializeFileMime(src, c)
				if err != nil || !reflect.DeepEqual(out.Fields(), want) {
					failures <- "concurrent materialization changed result"
					return
				}
				f := out.Fields()
				f.MimeTypeUTF16[0] = 'X'
				f.Checksums[0].Original[0] = 'X'
			}
		}(cat)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
	if src.Fields().MimeTypeUTF16 != nil {
		t.Fatal("concurrent reads changed unresolved source MIME")
	}
	mimeWant(t, src, cat, mimeUnits("text/plain"))
}

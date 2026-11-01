package localrepo

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
)

// 固定向量：空内容的真实 MD5/SHA256；填充摘要只需规范形状，不对账字节。
// Fixed vectors: real MD5/SHA256 of empty content; filler digests only need
// the canonical shape and are not reconciled against bytes here.
const (
	uploadTestEmptyMD5    = "d41d8cd98f00b204e9800998ecf8427e"
	uploadTestEmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// uploadTestUnits 把 ASCII 字符串转成 UTF-16 码元；空串交给调用方选择。
// uploadTestUnits converts an ASCII string to UTF-16 units; emptiness is the
// caller's explicit choice.
func uploadTestUnits(s string) []uint16 { return utf16.Encode([]rune(s)) }

// uploadTestMetadata 组装一份受控 FileMetadata；nil 码元即 null。
// uploadTestMetadata assembles one controllable FileMetadata; nil units mean null.
func uploadTestMetadata(created, modified, updated int64, createdBy, modifiedBy, sha1Original, md5Original []uint16) *repository.FileMetadata {
	metadata := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: created, ModifiedMillis: modified, UpdatedMillis: updated,
		CreatedBy:    repository.NewActor(createdBy),
		ModifiedBy:   repository.NewActor(modifiedBy),
		SHA1Original: sha1Original, MD5Original: md5Original,
	})
	return &metadata
}

// uploadTestSource 组装同查询 Source DTO；Actual 空性位默认全真。
// uploadTestSource assembles the same-query Source DTO with all Actual
// nullability flags true by default.
func uploadTestSource(id int64, path repository.Path, sha1, md5, sha256 string, size int64, metadata *repository.FileMetadata) repository.FileSnapshotSource {
	return repository.FileSnapshotSource{
		Reference: repository.FileReference{
			ID: id, Path: path,
			Content:  content.Metadata{SHA1: sha1, SHA256: sha256, MD5: md5, Size: size},
			Metadata: metadata,
		},
		SHA1ActualValid: true, MD5ActualValid: true, SHA256ActualValid: true,
	}
}

// uploadTestBaseSource 返回全字段合法的基线 Source。
// uploadTestBaseSource returns a fully valid baseline Source.
func uploadTestBaseSource(want repository.Path) repository.FileSnapshotSource {
	return uploadTestSource(7, want, strings.Repeat("a", 40), strings.Repeat("c", 32),
		strings.Repeat("b", 64), 9,
		uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("admin"), uploadTestUnits("deployer"), nil, nil))
}

func TestUploadConstantsAndSentinels(t *testing.T) {
	if MaxUploadMillis != 253402300799999 {
		t.Fatalf("MaxUploadMillis = %d, want 253402300799999", MaxUploadMillis)
	}
	sentinels := []error{ErrInvalidUploadInput, ErrInvalidUploadSource, ErrUploadPersistence}
	for _, sentinel := range sentinels {
		if sentinel == nil {
			t.Fatalf("upload sentinel must be nonnil")
		}
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Fatalf("upload sentinels must stay distinct: %v wraps %v", a, b)
			}
		}
	}
}

func TestNewUploadPathValid(t *testing.T) {
	tests := []struct {
		name   string
		repo   string
		path   string
		parent string
		file   string
		depth  int16
	}{
		{"root level bin", "libs-release", "app.bin", "", "app.bin", 1},
		{"txt suffix", "r", "f.txt", "", "f.txt", 1},
		{"nested", "r", "com/example/app-1.2.3_final.bin", "com/example", "app-1.2.3_final.bin", 3},
		{"double suffix stem", "r", "b.txt.bin", "", "b.txt.bin", 1},
		{"dot inside stem", "r", "d..bin", "", "d..bin", 1},
		{"repo max 64", strings.Repeat("k", 64), "a.bin", "", "a.bin", 1},
		{"parent max 1024 name max 255", "r", strings.Repeat("a/", 511) + "aa/" + strings.Repeat("c", 251) + ".bin", strings.Repeat("a/", 511) + "aa", strings.Repeat("c", 251) + ".bin", 513},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := NewUploadPath(tt.repo, tt.path)
			if err != nil {
				t.Fatalf("NewUploadPath(%q, %q) unexpected error: %v", tt.repo, tt.path, err)
			}
			if !path.IsFile() || path.IsRoot() {
				t.Fatalf("NewUploadPath must return a nonroot file path")
			}
			if path.Repo() != tt.repo || path.Parent() != tt.parent || path.Name() != tt.file {
				t.Fatalf("identity = (%q, %q, %q), want (%q, %q, %q)", path.Repo(), path.Parent(), path.Name(), tt.repo, tt.parent, tt.file)
			}
			depth, err := path.Depth()
			if err != nil || depth != tt.depth {
				t.Fatalf("depth = %d, %v, want %d", depth, err, tt.depth)
			}
		})
	}
}

func TestNewUploadPathInvalid(t *testing.T) {
	tests := []struct {
		name string
		repo string
		path string
	}{
		{"empty repo", "", "a.bin"},
		{"dot repo", ".", "a.bin"},
		{"dotdot repo", "..", "a.bin"},
		{"repo slash", "a/b", "a.bin"},
		{"repo space", "a b", "a.bin"},
		{"repo tab", "a\t", "a.bin"},
		{"repo nonascii", "r\xc3\xa9", "a.bin"},
		{"repo percent", "a%b", "a.bin"},
		{"repo colon", "a:b", "a.bin"},
		{"repo 65 bytes", strings.Repeat("k", 65), "a.bin"},
		{"empty path", "r", ""},
		{"leading slash", "r", "/a.bin"},
		{"trailing slash", "r", "a.bin/"},
		{"repeated slash", "r", "a//b.bin"},
		{"dot segment", "r", "a/./b.bin"},
		{"dotdot segment", "r", "a/../b.bin"},
		{"bare dot", "r", "."},
		{"no suffix", "r", "a/b"},
		{"suffix missing stem", "r", "a/.bin"},
		{"dot only with suffix", "r", ".bin"},
		{"uppercase suffix", "r", "a/b.BIN"},
		{"suffix not exact", "r", "a/b.binx"},
		{"suffix inside name", "r", "a/b.binx/"},
		{"segment space", "r", "a b/a.bin"},
		{"backslash", "r", `a\b.bin`},
		{"percent", "r", "a/%2e.bin"},
		{"tab inside", "r", "a/b\te.bin"},
		{"nonascii path", "r", "a/\xc3\xa9.bin"},
		{"trailing space", "r", "a.bin "},
		{"name 256 bytes", "r", "a/" + strings.Repeat("c", 252) + ".bin"},
		{"parent 1025 bytes", "r", strings.Repeat("a/", 513) + "x.bin"},
		{"path over 8192 bytes", "r", strings.Repeat("a/", 4100) + "x.bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, err := NewUploadPath(tt.repo, tt.path)
			if err == nil {
				t.Fatalf("NewUploadPath(%q, %q) = %+v, want rejection", tt.repo, tt.path, path)
			}
			if !errors.Is(err, ErrInvalidUploadInput) {
				t.Fatalf("NewUploadPath error = %v, want ErrInvalidUploadInput", err)
			}
			if path != (repository.Path{}) {
				t.Fatalf("rejected NewUploadPath must return the zero Path")
			}
		})
	}
}

func TestValidateUploadInputValid(t *testing.T) {
	path, err := NewUploadPath("libs-release", "com/example/app.bin")
	if err != nil {
		t.Fatalf("NewUploadPath failed: %v", err)
	}
	manual, err := repository.NewPath("libs-release", "com/example", "app.bin", true)
	if err != nil {
		t.Fatalf("NewPath failed: %v", err)
	}
	tests := []struct {
		name string
		in   UploadInput
	}{
		{"minimal", UploadInput{Path: path, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 0}},
		{"manual identity equal", UploadInput{Path: manual, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 0}},
		{"checksums all set", UploadInput{Path: path, Checksums: repository.ClientFileChecksums{
			SHA1Original:   uploadTestUnits(strings.Repeat("a", 40)),
			SHA256Original: uploadTestUnits(strings.Repeat("b", 64)),
			MD5Original:    uploadTestUnits(strings.Repeat("c", 32)),
		}, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 1 << 40}},
		{"operator single unit", UploadInput{Path: path, OperatorUTF16: []uint16{'x'}, DeclaredSize: 1}},
		{"operator 64 units not all spaces", UploadInput{Path: path, OperatorUTF16: append(uploadTestUnits(strings.Repeat(" ", 63)), 'x'), DeclaredSize: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateUploadInput(tt.in); err != nil {
				t.Fatalf("ValidateUploadInput unexpected error: %v", err)
			}
		})
	}
}

func TestValidateUploadInputInvalid(t *testing.T) {
	path, err := NewUploadPath("libs-release", "com/example/app.bin")
	if err != nil {
		t.Fatalf("NewUploadPath failed: %v", err)
	}
	folder, err := repository.NewPath("libs-release", "com/example", "app.bin", false)
	if err != nil {
		t.Fatalf("NewPath folder failed: %v", err)
	}
	root, err := repository.NewPath("libs-release", "", "", true)
	if err != nil {
		t.Fatalf("NewPath root failed: %v", err)
	}
	doubleSlash, err := repository.NewPath("r", "a//b", "c.bin", true)
	if err != nil {
		t.Fatalf("NewPath double slash failed: %v", err)
	}
	dotParent, err := repository.NewPath("r", "a/./b", "c.bin", true)
	if err != nil {
		t.Fatalf("NewPath dot parent failed: %v", err)
	}
	noSuffix, err := repository.NewPath("r", "a", "b", true)
	if err != nil {
		t.Fatalf("NewPath no suffix failed: %v", err)
	}
	emptyStem, err := repository.NewPath("r", "a", ".bin", true)
	if err != nil {
		t.Fatalf("NewPath empty stem failed: %v", err)
	}
	upperSuffix, err := repository.NewPath("r", "a", "b.BIN", true)
	if err != nil {
		t.Fatalf("NewPath upper suffix failed: %v", err)
	}
	nonASCII, err := repository.NewPath("r", "\xc3\xa9", "b.bin", true)
	if err != nil {
		t.Fatalf("NewPath nonascii failed: %v", err)
	}
	deep, err := repository.NewPath("r", strings.Repeat("a/", 513)+"x", "c.bin", true)
	if err != nil {
		t.Fatalf("NewPath deep failed: %v", err)
	}
	base := UploadInput{Path: path, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}
	tests := []struct {
		name string
		in   UploadInput
	}{
		{"zero path", UploadInput{OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"folder path", UploadInput{Path: folder, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"root path", UploadInput{Path: root, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"parent double slash", UploadInput{Path: doubleSlash, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"parent dot segment", UploadInput{Path: dotParent, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"name without suffix", UploadInput{Path: noSuffix, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"name empty stem", UploadInput{Path: emptyStem, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"name uppercase suffix", UploadInput{Path: upperSuffix, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"nonascii parent", UploadInput{Path: nonASCII, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"parent over 1024", UploadInput{Path: deep, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"sha1 original uppercase", UploadInput{Path: path, Checksums: repository.ClientFileChecksums{SHA1Original: uploadTestUnits(strings.Repeat("A", 40))}, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"sha256 original short", UploadInput{Path: path, Checksums: repository.ClientFileChecksums{SHA256Original: uploadTestUnits(strings.Repeat("b", 63))}, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"md5 original empty present", UploadInput{Path: path, Checksums: repository.ClientFileChecksums{MD5Original: []uint16{}}, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"md5 original nonhex", UploadInput{Path: path, Checksums: repository.ClientFileChecksums{MD5Original: uploadTestUnits(strings.Repeat("g", 32))}, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"sha1 original marker", UploadInput{Path: path, Checksums: repository.ClientFileChecksums{SHA1Original: uploadTestUnits("NO_ORIG")}, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"sha1 original length 41", UploadInput{Path: path, Checksums: repository.ClientFileChecksums{SHA1Original: uploadTestUnits(strings.Repeat("a", 41))}, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: 3}},
		{"operator nil", UploadInput{Path: path, DeclaredSize: 3}},
		{"operator empty", UploadInput{Path: path, OperatorUTF16: []uint16{}, DeclaredSize: 3}},
		{"operator all spaces", UploadInput{Path: path, OperatorUTF16: uploadTestUnits("   "), DeclaredSize: 3}},
		{"operator 65 units", UploadInput{Path: path, OperatorUTF16: uploadTestUnits(strings.Repeat("A", 65)), DeclaredSize: 3}},
		{"operator del unit", UploadInput{Path: path, OperatorUTF16: []uint16{0x7f}, DeclaredSize: 3}},
		{"operator control unit", UploadInput{Path: path, OperatorUTF16: []uint16{0x1f}, DeclaredSize: 3}},
		{"operator nonascii unit", UploadInput{Path: path, OperatorUTF16: []uint16{0xe9}, DeclaredSize: 3}},
		{"operator tab unit", UploadInput{Path: path, OperatorUTF16: []uint16{'\t'}, DeclaredSize: 3}},
		{"declared size negative", UploadInput{Path: path, OperatorUTF16: uploadTestUnits("ci-bot"), DeclaredSize: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUploadInput(tt.in)
			if err == nil {
				t.Fatalf("ValidateUploadInput accepted invalid input %+v", tt.in)
			}
			if !errors.Is(err, ErrInvalidUploadInput) {
				t.Fatalf("ValidateUploadInput error = %v, want ErrInvalidUploadInput", err)
			}
			if errors.Is(err, ErrInvalidUploadSource) || errors.Is(err, ErrUploadPersistence) {
				t.Fatalf("input rejection must not wrap other upload sentinels: %v", err)
			}
		})
	}
	if err := ValidateUploadInput(base); err != nil {
		t.Fatalf("baseline input must stay valid: %v", err)
	}
}

func TestValidateUploadInputDoesNotMutate(t *testing.T) {
	path, err := NewUploadPath("libs-release", "com/example/app.bin")
	if err != nil {
		t.Fatalf("NewUploadPath failed: %v", err)
	}
	in := UploadInput{
		Path: path,
		Checksums: repository.ClientFileChecksums{
			SHA1Original:   uploadTestUnits(strings.Repeat("a", 40)),
			SHA256Original: uploadTestUnits(strings.Repeat("b", 64)),
			MD5Original:    uploadTestUnits(strings.Repeat("c", 32)),
		},
		OperatorUTF16: uploadTestUnits("ci-bot"),
		DeclaredSize:  5,
	}
	before := UploadInput{
		Path: in.Path,
		Checksums: repository.ClientFileChecksums{
			SHA1Original:   append([]uint16(nil), in.Checksums.SHA1Original...),
			SHA256Original: append([]uint16(nil), in.Checksums.SHA256Original...),
			MD5Original:    append([]uint16(nil), in.Checksums.MD5Original...),
		},
		OperatorUTF16: append([]uint16(nil), in.OperatorUTF16...),
		DeclaredSize:  in.DeclaredSize,
	}
	for i := 0; i < 2; i++ {
		if err := ValidateUploadInput(in); err != nil {
			t.Fatalf("ValidateUploadInput pass %d failed: %v", i, err)
		}
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("ValidateUploadInput mutated its input: %+v != %+v", in, before)
	}
}

func TestValidateUploadSourceValid(t *testing.T) {
	want, err := NewUploadPath("libs-release", "com/example/app.bin")
	if err != nil {
		t.Fatalf("NewUploadPath failed: %v", err)
	}
	fillerSHA1, fillerMD5, fillerSHA256 := strings.Repeat("a", 40), strings.Repeat("c", 32), strings.Repeat("b", 64)
	tests := []struct {
		name   string
		source repository.FileSnapshotSource
	}{
		{"empty content", uploadTestSource(7, want, emptyFileSHA1, uploadTestEmptyMD5, uploadTestEmptySHA256, 0,
			uploadTestMetadata(0, 0, 0, uploadTestUnits("admin"), uploadTestUnits("admin"), nil, nil))},
		{"filler digests", uploadTestBaseSource(want)},
		{"originals match actual", uploadTestSource(1, want, fillerSHA1, fillerMD5, fillerSHA256, 9,
			uploadTestMetadata(1, 2, 3, uploadTestUnits("a"), uploadTestUnits("b"), uploadTestUnits(fillerSHA1), uploadTestUnits(fillerMD5)))},
		{"millis at maximum", uploadTestSource(2, want, fillerSHA1, fillerMD5, fillerSHA256, 9,
			uploadTestMetadata(MaxUploadMillis, MaxUploadMillis, MaxUploadMillis, uploadTestUnits("a"), uploadTestUnits("b"), nil, nil))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateUploadSource(tt.source, want); err != nil {
				t.Fatalf("ValidateUploadSource unexpected error: %v", err)
			}
		})
	}
}

func TestValidateUploadSourceInvalid(t *testing.T) {
	want, err := NewUploadPath("libs-release", "com/example/app.bin")
	if err != nil {
		t.Fatalf("NewUploadPath failed: %v", err)
	}
	fillerSHA1, fillerMD5, fillerSHA256 := strings.Repeat("a", 40), strings.Repeat("c", 32), strings.Repeat("b", 64)
	base := func() repository.FileSnapshotSource { return uploadTestBaseSource(want) }
	folderSameKey, err := repository.NewPath("libs-release", "com/example", "app.bin", false)
	if err != nil {
		t.Fatalf("NewPath folder failed: %v", err)
	}
	otherName, err := NewUploadPath("libs-release", "com/example/other.bin")
	if err != nil {
		t.Fatalf("NewUploadPath other failed: %v", err)
	}
	otherRepo, err := NewUploadPath("libs-snapshot", "com/example/app.bin")
	if err != nil {
		t.Fatalf("NewUploadPath other repo failed: %v", err)
	}
	noSuffixWant, err := repository.NewPath("libs-release", "com/example", "app", true)
	if err != nil {
		t.Fatalf("NewPath no suffix failed: %v", err)
	}
	withPath := func(p repository.Path) repository.FileSnapshotSource {
		source := base()
		source.Reference.Path = p
		return source
	}
	withID := func(id int64) repository.FileSnapshotSource {
		source := base()
		source.Reference.ID = id
		return source
	}
	withValid := func(sha1, md5, sha256 bool) repository.FileSnapshotSource {
		source := base()
		source.SHA1ActualValid, source.MD5ActualValid, source.SHA256ActualValid = sha1, md5, sha256
		return source
	}
	withContent := func(sha1, md5, sha256 string, size int64) repository.FileSnapshotSource {
		source := base()
		source.Reference.Content = content.Metadata{SHA1: sha1, SHA256: sha256, MD5: md5, Size: size}
		return source
	}
	withMetadata := func(m *repository.FileMetadata) repository.FileSnapshotSource {
		source := base()
		source.Reference.Metadata = m
		return source
	}
	actorMetadata := func(createdBy, modifiedBy []uint16) *repository.FileMetadata {
		return uploadTestMetadata(1000, 2000, 3000, createdBy, modifiedBy, nil, nil)
	}
	tests := []struct {
		name   string
		source repository.FileSnapshotSource
		want   repository.Path
	}{
		{"zero id", withID(0), want},
		{"negative id", withID(-3), want},
		{"different name", withPath(otherName), want},
		{"different repo", withPath(otherRepo), want},
		{"folder same key", withPath(folderSameKey), want},
		{"root path", withPath(repository.Path{}), want},
		{"sha1 actual null", withValid(false, true, true), want},
		{"md5 actual null", withValid(true, false, true), want},
		{"sha256 actual null", withValid(true, true, false), want},
		{"sha1 uppercase", withContent(strings.Repeat("A", 40), fillerMD5, fillerSHA256, 9), want},
		{"sha1 short", withContent(strings.Repeat("a", 39), fillerMD5, fillerSHA256, 9), want},
		{"sha1 nonhex", withContent(strings.Repeat("g", 40), fillerMD5, fillerSHA256, 9), want},
		{"sha1 empty string", withContent("", fillerMD5, fillerSHA256, 9), want},
		{"md5 nonhex", withContent(fillerSHA1, strings.Repeat("g", 32), fillerSHA256, 9), want},
		{"sha256 nonhex", withContent(fillerSHA1, fillerMD5, strings.Repeat("z", 64), 9), want},
		{"negative size", withContent(fillerSHA1, fillerMD5, fillerSHA256, -1), want},
		{"empty sha1 with nonzero size", withContent(emptyFileSHA1, fillerMD5, fillerSHA256, 1), want},
		{"metadata nil", func() repository.FileSnapshotSource {
			source := base()
			source.Reference.Metadata = nil
			return source
		}(), want},
		{"created millis negative", withMetadata(uploadTestMetadata(-1, 2000, 3000, uploadTestUnits("a"), uploadTestUnits("b"), nil, nil)), want},
		{"modified millis over max", withMetadata(uploadTestMetadata(1000, MaxUploadMillis+1, 3000, uploadTestUnits("a"), uploadTestUnits("b"), nil, nil)), want},
		{"updated millis over max", withMetadata(uploadTestMetadata(1000, 2000, MaxUploadMillis+1, uploadTestUnits("a"), uploadTestUnits("b"), nil, nil)), want},
		{"updated millis negative", withMetadata(uploadTestMetadata(1000, 2000, -1, uploadTestUnits("a"), uploadTestUnits("b"), nil, nil)), want},
		{"created by null", withMetadata(actorMetadata(nil, uploadTestUnits("b"))), want},
		{"created by empty", withMetadata(actorMetadata([]uint16{}, uploadTestUnits("b"))), want},
		{"created by all spaces", withMetadata(actorMetadata(uploadTestUnits("   "), uploadTestUnits("b"))), want},
		{"created by nonascii", withMetadata(actorMetadata(uploadTestUnits("éa"), uploadTestUnits("b"))), want},
		{"created by control", withMetadata(actorMetadata([]uint16{'a', 0x1f}, uploadTestUnits("b"))), want},
		{"modified by null", withMetadata(actorMetadata(uploadTestUnits("a"), nil)), want},
		{"modified by empty", withMetadata(actorMetadata(uploadTestUnits("a"), []uint16{})), want},
		{"modified by all spaces", withMetadata(actorMetadata(uploadTestUnits("a"), uploadTestUnits("  "))), want},
		{"modified by nonascii", withMetadata(actorMetadata(uploadTestUnits("a"), uploadTestUnits("b中"))), want},
		{"sha1 original empty present", withMetadata(uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("a"), uploadTestUnits("b"), []uint16{}, nil)), want},
		{"sha1 original uppercase", withMetadata(uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("a"), uploadTestUnits("b"), uploadTestUnits(strings.Repeat("A", 40)), nil)), want},
		{"sha1 original mismatch", withMetadata(uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("a"), uploadTestUnits("b"), uploadTestUnits(strings.Repeat("d", 40)), nil)), want},
		{"sha1 original marker", withMetadata(uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("a"), uploadTestUnits("b"), uploadTestUnits("NO_ORIG"), nil)), want},
		{"md5 original mismatch", withMetadata(uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("a"), uploadTestUnits("b"), nil, uploadTestUnits(strings.Repeat("d", 32)))), want},
		{"md5 original uppercase", withMetadata(uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("a"), uploadTestUnits("b"), nil, uploadTestUnits(strings.Repeat("C", 32)))), want},
		{"want zero path", base(), repository.Path{}},
		{"want folder", base(), folderSameKey},
		{"want outside profile", base(), noSuffixWant},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUploadSource(tt.source, tt.want)
			if err == nil {
				t.Fatalf("ValidateUploadSource accepted invalid source")
			}
			if !errors.Is(err, ErrInvalidUploadSource) {
				t.Fatalf("ValidateUploadSource error = %v, want ErrInvalidUploadSource", err)
			}
			if errors.Is(err, ErrInvalidUploadInput) || errors.Is(err, ErrUploadPersistence) {
				t.Fatalf("source rejection must not wrap other upload sentinels: %v", err)
			}
		})
	}
	if err := ValidateUploadSource(base(), want); err != nil {
		t.Fatalf("baseline source must stay valid: %v", err)
	}
}

func TestValidateUploadSourceDoesNotMutate(t *testing.T) {
	want, err := NewUploadPath("libs-release", "com/example/app.bin")
	if err != nil {
		t.Fatalf("NewUploadPath failed: %v", err)
	}
	fillerSHA1, fillerMD5, fillerSHA256 := strings.Repeat("a", 40), strings.Repeat("c", 32), strings.Repeat("b", 64)
	metadata := uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("admin"), uploadTestUnits("deployer"), uploadTestUnits(fillerSHA1), uploadTestUnits(fillerMD5))
	source := uploadTestSource(9, want, fillerSHA1, fillerMD5, fillerSHA256, 11, metadata)
	clone := uploadTestSource(9, want, fillerSHA1, fillerMD5, fillerSHA256, 11,
		uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("admin"), uploadTestUnits("deployer"), uploadTestUnits(fillerSHA1), uploadTestUnits(fillerMD5)))
	if err := ValidateUploadSource(source, want); err != nil {
		t.Fatalf("ValidateUploadSource failed: %v", err)
	}
	if !reflect.DeepEqual(source, clone) {
		t.Fatalf("ValidateUploadSource mutated its source DTO")
	}
	// 拒绝路径同样不得改动输入。
	// The rejection path must not mutate its input either.
	broken := uploadTestSource(0, want, fillerSHA1, fillerMD5, fillerSHA256, 11, metadata)
	brokenClone := uploadTestSource(0, want, fillerSHA1, fillerMD5, fillerSHA256, 11,
		uploadTestMetadata(1000, 2000, 3000, uploadTestUnits("admin"), uploadTestUnits("deployer"), uploadTestUnits(fillerSHA1), uploadTestUnits(fillerMD5)))
	if err := ValidateUploadSource(broken, want); err == nil {
		t.Fatalf("broken source must be rejected")
	}
	if !reflect.DeepEqual(broken, brokenClone) {
		t.Fatalf("ValidateUploadSource mutated a rejected source DTO")
	}
}

func TestUploadErrorUnwrap(t *testing.T) {
	path, err := NewUploadPath("libs-release", "com/example/app.bin")
	if err != nil {
		t.Fatalf("NewUploadPath failed: %v", err)
	}
	inputErr := ValidateUploadInput(UploadInput{Path: path, DeclaredSize: -1})
	if inputErr == nil || errors.Unwrap(inputErr) != ErrInvalidUploadInput {
		t.Fatalf("ValidateUploadInput error chain = %v, want Unwrap -> ErrInvalidUploadInput", inputErr)
	}
	sourceErr := ValidateUploadSource(repository.FileSnapshotSource{}, path)
	if sourceErr == nil || errors.Unwrap(sourceErr) != ErrInvalidUploadSource {
		t.Fatalf("ValidateUploadSource error chain = %v, want Unwrap -> ErrInvalidUploadSource", sourceErr)
	}
	badPath, pathErr := NewUploadPath("r", "no-suffix")
	if badPath != (repository.Path{}) || pathErr == nil || errors.Unwrap(pathErr) != ErrInvalidUploadInput {
		t.Fatalf("NewUploadPath rejection chain = %v, want Unwrap -> ErrInvalidUploadInput", pathErr)
	}
}

// 字符状态机能拒绝普通段过滤尚未排除的连续三点，不能被长度测试遮蔽。
// The storage character machine rejects triple dots still admitted by the
// basic segment filter, independently of the byte-length guards.
func TestUploadStoragePathGuard(t *testing.T) {
	for _, tt := range []struct{ repo, parent string }{{"...", ""}, {"r", "..."}} {
		path, err := repository.NewPath(tt.repo, tt.parent, "a.bin", true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewUploadPath(tt.repo, path.PathName()); !errors.Is(err, ErrInvalidUploadInput) {
			t.Fatalf("storage character guard missing from constructor: %v", err)
		}
		in := UploadInput{Path: path, OperatorUTF16: uploadTestUnits("bot"), DeclaredSize: 3}
		if err := ValidateUploadInput(in); !errors.Is(err, ErrInvalidUploadInput) {
			t.Fatalf("storage character guard missing from input: %v", err)
		}
		if err := ValidateUploadSource(uploadTestBaseSource(path), path); !errors.Is(err, ErrInvalidUploadSource) {
			t.Fatalf("storage character guard missing from source: %v", err)
		}
	}
}

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

// 内存端口只验证命令边界与顺序，不代表真实 PostgreSQL 验收。
// These in-memory ports prove command boundaries and order, not PostgreSQL behavior.
type publishedSaveStore struct {
	source              FileSnapshotSource
	lookupErr, writeErr error
	count               int64
	query               Path
	written             FileReference
	events              *[]string
	onLookup            func()
}

func (s *publishedSaveStore) LookupSnapshotSource(_ context.Context, query Path) (FileSnapshotSource, error) {
	*s.events = append(*s.events, "lookup")
	s.query = query
	s.onLookup()
	return s.source, s.lookupErr
}
func (s *publishedSaveStore) Create(_ context.Context, ref FileReference) (int64, error) {
	return s.write("create", ref)
}
func (s *publishedSaveStore) Update(_ context.Context, ref FileReference) (int64, error) {
	return s.write("update", ref)
}
func (s *publishedSaveStore) write(op string, ref FileReference) (int64, error) {
	*s.events = append(*s.events, op)
	s.written = ref
	return s.count, s.writeErr
}

type publishedSaveClock struct {
	values []int64
	calls  int
	events *[]string
	onRead func(int)
}

func (c *publishedSaveClock) NowMillis() int64 {
	*c.events = append(*c.events, "clock")
	c.calls++
	c.onRead(c.calls)
	return c.values[(c.calls-1)%len(c.values)]
}
func publishedSaveFixture() (LocalFileSaveInput, LocalFileSaveDependencies, *publishedSaveStore, *publishedSaveClock, *[]string) {
	events := new([]string)
	s := &publishedSaveStore{lookupErr: sql.ErrNoRows, count: 1, events: events, onLookup: func() {}}
	c := &publishedSaveClock{values: []int64{101, -53}, events: events, onRead: func(int) {}}
	in := LocalFileSaveInput{Repo: "rk", Path: "dir/file.bin", Published: loadedBinaryA(), ChecksumPolicy: LocalFileChecksumClient,
		CurrentUserUTF16: []uint16{'u'}, Checksums: ClientFileChecksums{SHA1Original: loadedUpdateUnits(strings.Repeat("A", 40)), SHA256Original: loadedUpdateUnits("NO_ORIG"), MD5Original: []uint16{}}}
	d := LocalFileSaveDependencies{Store: s, Clock: c, NextID: func(context.Context) (int64, error) { *events = append(*events, "id"); return 73, nil }}
	return in, d, s, c, events
}
func publishedSaveError(t *testing.T, got LocalFileSaveResult, err, cause error) {
	t.Helper()
	if got != (LocalFileSaveResult{}) || err == nil || (cause != nil && !errors.Is(err, cause)) {
		t.Fatalf("result=%v error=%v, want zero and cause %v", got, err, cause)
	}
	publishedSaveRequire(t, !strings.Contains(err.Error(), "private-marker"), "error text exposes input or underlying cause")
}
func publishedSaveRequire(t *testing.T, ok bool, message string) {
	t.Helper()
	if !ok {
		t.Fatal(message)
	}
}
func publishedSaveEvents(t *testing.T, got *[]string, want ...string) {
	t.Helper()
	publishedSaveRequire(t, reflect.DeepEqual(*got, want), fmt.Sprintf("events=%v want=%v", *got, want))
}
func TestSavePublishedLocalFreshMetadataAndClockFacts(t *testing.T) {
	for _, created := range []int64{-7, 0, 123} {
		for _, modified := range [][2]int64{{-1, 101}, {0, 101}, {44, 44}} {
			in, d, s, _, events := publishedSaveFixture()
			in.ResourceCreatedMillis, in.ResourceModifiedMillis = created, modified[0]
			in.CreatedPolicy, in.OriginalCase = 0, 255
			got, err := SavePublishedLocalFile(context.Background(), in, d)
			publishedSaveRequire(t, err == nil && got == (LocalFileSaveResult{ID: 73, Created: true, Changed: true, Count: 1}), fmt.Sprintf("result=%v err=%v", got, err))
			publishedSaveEvents(t, events, "lookup", "clock", "clock", "id", "create")
			m := s.written.Metadata
			wantModified := modified[1]
			publishedSaveRequire(t, m != nil, "fresh metadata missing")
			if s.written.ID != 73 || s.written.Content != (content.Metadata{SHA1: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64), MD5: strings.Repeat("e", 32), Size: 7}) || m.CreatedMillis() != created || m.ModifiedMillis() != wantModified || m.UpdatedMillis() != -53 ||
				!reflect.DeepEqual(m.CreatedBy().UTF16(), []uint16{'u'}) || !reflect.DeepEqual(m.ModifiedBy().UTF16(), []uint16{'u'}) ||
				!reflect.DeepEqual(m.SHA1OriginalUTF16(), loadedUpdateUnits(strings.Repeat("a", 40))) || !reflect.DeepEqual(m.MD5OriginalUTF16(), []uint16{}) {
				t.Fatal("fresh seven fields, actuals or independent clock facts differ")
			}
		}
	}
}
func TestSavePublishedLocalFreshOriginalPolicies(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw, want []uint16
		policy    LocalFileChecksumPolicy
	}{
		{"null", nil, nil, LocalFileChecksumClient}, {"empty", []uint16{}, []uint16{}, LocalFileChecksumClient},
		{"blank", []uint16{' ', '\t'}, []uint16{' ', '\t'}, LocalFileChecksumClient}, {"marker", loadedUpdateUnits("NO_ORIG"), loadedUpdateUnits("NO_ORIG"), LocalFileChecksumClient},
		{"upper", loadedUpdateUnits(strings.Repeat("A", 40)), loadedUpdateUnits(strings.Repeat("a", 40)), LocalFileChecksumClient},
		{"server raw", []uint16{'X', 0xd800}, []uint16{'X', 0xd800}, LocalFileChecksumServer},
	} {
		in, d, s, _, _ := publishedSaveFixture()
		in.ChecksumPolicy = tc.policy
		in.Checksums.SHA1Original = tc.raw
		if tc.policy == LocalFileChecksumServer {
			in.Checksums.SHA256Original = []uint16{'X'}
			in.Checksums.MD5Original = []uint16{'Y', 0xdc00}
		}
		_, err := SavePublishedLocalFile(context.Background(), in, d)
		publishedSaveRequire(t, err == nil, fmt.Sprintf("save error=%v", err))
		publishedSaveRequire(t, reflect.DeepEqual(s.written.Metadata.SHA1OriginalUTF16(), tc.want), "Original normalization or input ownership differs")
		if tc.policy == LocalFileChecksumServer && !reflect.DeepEqual(s.written.Metadata.MD5OriginalUTF16(), []uint16{'Y', 0xdc00}) {
			t.Fatal("SERVER must preserve raw mismatching MD5")
		}
	}
}
func TestSavePublishedLocalFreshActorsProjectWithoutMutatingRawInput(t *testing.T) {
	long := append(loadedUpdateUnits(strings.Repeat("x", 60)), 0xd83d, 0xde00)
	long = append(long, loadedUpdateUnits("abcdefghij")...)
	wantLong := append(loadedUpdateUnits(strings.Repeat("x", 60)), 0xd83d, '.', '.', '.')
	for _, tc := range []struct{ raw, want []uint16 }{{long, wantLong}, {[]uint16{'a', 0xd800, 'b', 0xdc00}, []uint16{'a', 0xd800, 'b', 0xdc00}}} {
		in, d, s, _, _ := publishedSaveFixture()
		in.CurrentUserUTF16 = tc.raw
		before := append([]uint16(nil), tc.raw...)
		_, err := SavePublishedLocalFile(context.Background(), in, d)
		publishedSaveRequire(t, err == nil, fmt.Sprintf("save error=%v", err))
		m := s.written.Metadata
		publishedSaveRequire(t, reflect.DeepEqual(m.CreatedBy().UTF16(), tc.want) && reflect.DeepEqual(m.ModifiedBy().UTF16(), tc.want) && reflect.DeepEqual(tc.raw, before), "actor projection must abbreviate UTF-16 without modifying the raw principal")
		tc.raw[0] = '!'
		publishedSaveRequire(t, reflect.DeepEqual(m.CreatedBy().UTF16(), tc.want), "actor retains caller backing storage")
	}
}
func TestSavePublishedLocalFreshRejectsBeforeUpdatedAllocationAndInsert(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*LocalFileSaveInput)
		want error
	}{
		{"SHA1", func(in *LocalFileSaveInput) { in.Checksums.SHA1Original = []uint16{'X'} }, ErrClientChecksumMismatch},
		{"SHA256", func(in *LocalFileSaveInput) { in.Checksums.SHA256Original = []uint16{'X'} }, ErrClientChecksumMismatch},
		{"MD5", func(in *LocalFileSaveInput) { in.Checksums.MD5Original = []uint16{'X'} }, ErrClientChecksumMismatch},
		{"policy", func(in *LocalFileSaveInput) { in.ChecksumPolicy = 0 }, ErrUnsupportedLocalFileChecksumPolicy},
		{"published", func(in *LocalFileSaveInput) { in.Published.SHA1 = "private-marker" }, ErrInvalidPublishedFileMetadata},
	} {
		in, d, _, _, events := publishedSaveFixture()
		tc.edit(&in)
		got, err := SavePublishedLocalFile(context.Background(), in, d)
		publishedSaveError(t, got, err, tc.want)
		publishedSaveEvents(t, events, "lookup", "clock")
	}
}
func TestSavePublishedLocalPathGuardsPrecedeLookup(t *testing.T) {
	for _, tc := range []struct {
		path string
		want error
	}{
		{"", ErrInvalidPath}, {"/", ErrInvalidPath}, {" \t ", ErrInvalidPath}, {"dir/f\x00private-marker", ErrInvalidPath}, {"dir/\xff", ErrInvalidPath},
		{"a/ /", ErrUnsupportedLocalFileSavePath}, {"/ a/", ErrUnsupportedLocalFileSavePath}, {"/a /", ErrUnsupportedLocalFileSavePath}, {"a/ / /b", ErrUnsupportedLocalFileSavePath},
		{"dir/:properties", ErrInvalidPath}, {":properties", ErrInvalidPath}, {".", ErrInvalidStoragePath}, {"./x", ErrInvalidStoragePath}, {".:properties", ErrUnsupportedLocalFileSavePath},
	} {
		in, d, _, _, events := publishedSaveFixture()
		in.Path = tc.path
		got, err := SavePublishedLocalFile(context.Background(), in, d)
		publishedSaveError(t, got, err, tc.want)
		publishedSaveEvents(t, events)
	}
	in, d, _, _, events := publishedSaveFixture()
	in.Repo = "rk\xff"
	got, err := SavePublishedLocalFile(context.Background(), in, d)
	publishedSaveError(t, got, err, ErrInvalidPath)
	publishedSaveEvents(t, events)
}
func TestSavePublishedLocalLookupAndCreateShareOneMappedIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{{"dir/f:properties:statistics", "dir/f:properties"}, {"a/", "a"}, {"a//", "a"}} {
		in, d, s, _, events := publishedSaveFixture()
		in.Path = tc.raw
		s.source.Reference.ID = 999
		s.lookupErr = fmt.Errorf("wrapped: %w", sql.ErrNoRows)
		got, err := SavePublishedLocalFile(context.Background(), in, d)
		publishedSaveRequire(t, err == nil && got.ID == 73, fmt.Sprintf("result=%v err=%v", got, err))
		publishedSaveRequire(t, s.query.Repo() == "rk" && s.query.PathName() == tc.want && s.written.Path == s.query, "lookup/insert identity differs")
		publishedSaveEvents(t, events, "lookup", "clock", "clock", "id", "create")
	}
}
func TestSavePublishedLocalLookupFailureAndInvalidHitNeverCreate(t *testing.T) {
	cause := errors.New("private-marker folder or database failure")
	for _, state := range []string{"lookup failure", "legacy metadata", "invalid id"} {
		in, d, s, _, events := publishedSaveFixture()
		s.source, d.Catalog = loadedBinaryFixture(t)
		s.lookupErr = nil
		in.CreatedPolicy = PreserveLoadedFileCreated
		want := ErrInvalidFileSnapshotSource
		switch state {
		case "lookup failure":
			s.lookupErr = fmt.Errorf("outer: %w", cause)
			want = cause
		case "legacy metadata":
			s.source.Reference.Metadata = nil
		case "invalid id":
			s.source.Reference.ID, want = 0, ErrInvalidUpdateID
		}
		got, err := SavePublishedLocalFile(context.Background(), in, d)
		publishedSaveError(t, got, err, want)
		publishedSaveEvents(t, events, "lookup")
	}
}
func TestSavePublishedLocalHitUsesStoredIdentityAndDistinguishesSkipFromZeroRows(t *testing.T) {
	cause := errors.New("private-marker write failure")
	for _, tc := range []struct {
		name         string
		clock, count int64
		changed      bool
		fail         error
	}{
		{"skip", 33, 0, false, nil}, {"changed", 44, 1, true, nil}, {"zero rows", 44, 0, true, nil}, {"write failure", 44, 1, true, cause},
	} {
		in, d, s, c, events := publishedSaveFixture()
		s.source, d.Catalog = loadedBinaryFixture(t)
		s.lookupErr = nil
		s.source.Reference.ID = 93
		in.Repo, in.Path = "request", "different.bin"
		in.CreatedPolicy = PreserveLoadedFileCreated
		in.CurrentUserUTF16 = loadedUpdateUnits("modifier")
		in.ResourceModifiedMillis = 22
		in.Checksums.MD5Original = nil
		c.values = []int64{tc.clock}
		s.count, s.writeErr = tc.count, tc.fail
		got, err := SavePublishedLocalFile(context.Background(), in, d)
		if tc.fail != nil {
			publishedSaveError(t, got, err, tc.fail)
		} else {
			publishedSaveRequire(t, err == nil && got == (LocalFileSaveResult{ID: 93, Changed: tc.changed, Count: tc.count}), fmt.Sprintf("result=%v err=%v", got, err))
		}
		publishedSaveRequire(t, s.query.Repo() == "request" && s.query.PathName() == "different.bin", "request lookup key lost")
		if !tc.changed {
			publishedSaveEvents(t, events, "lookup", "clock")
		} else {
			publishedSaveEvents(t, events, "lookup", "clock", "update")
			publishedSaveRequire(t, s.written.ID == 93 && s.written.Path == s.source.Reference.Path, "hit must use authoritative stored identity")
		}
	}
}
func TestSavePublishedLocalHitValidationPrecedesClock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*LocalFileSaveInput, *LocalFileSaveDependencies)
	}{
		{"checksum", func(in *LocalFileSaveInput, _ *LocalFileSaveDependencies) {
			in.Checksums.SHA256Original = []uint16{'X'}
		}},
		{"created policy", func(in *LocalFileSaveInput, _ *LocalFileSaveDependencies) { in.CreatedPolicy = 0 }},
		{"original case", func(in *LocalFileSaveInput, _ *LocalFileSaveDependencies) { in.OriginalCase = 255 }},
		{"catalog", func(_ *LocalFileSaveInput, d *LocalFileSaveDependencies) { d.Catalog = FileMimeCatalog{} }},
	} {
		in, d, s, _, events := publishedSaveFixture()
		s.source, d.Catalog = loadedBinaryFixture(t)
		s.lookupErr = nil
		in.CreatedPolicy = PreserveLoadedFileCreated
		tc.change(&in, &d)
		got, err := SavePublishedLocalFile(context.Background(), in, d)
		publishedSaveError(t, got, err, nil)
		publishedSaveEvents(t, events, "lookup")
	}
}
func TestSavePublishedLocalCreateFailureDoesNotRetry(t *testing.T) {
	cause := errors.New("private-marker duplicate key or allocation failure")
	for _, tc := range []struct {
		name                      string
		id, count                 int64
		allocErr, insertErr, want error
	}{
		{"zero id", 0, 1, nil, nil, ErrInvalidAllocatedID}, {"negative id", -7, 1, nil, nil, ErrInvalidAllocatedID},
		{"allocation error", 73, 1, cause, nil, cause}, {"zero count", 73, 0, nil, nil, ErrUnexpectedCreateCount},
		{"two count", 73, 2, nil, nil, ErrUnexpectedCreateCount}, {"duplicate key", 73, 1, nil, cause, cause},
	} {
		in, d, s, _, events := publishedSaveFixture()
		s.count, s.writeErr = tc.count, tc.insertErr
		d.NextID = func(context.Context) (int64, error) { *events = append(*events, "id"); return tc.id, tc.allocErr }
		got, err := SavePublishedLocalFile(context.Background(), in, d)
		publishedSaveError(t, got, err, tc.want)
		wantEvents := []string{"lookup", "clock", "clock", "id"}
		if tc.id > 0 && tc.allocErr == nil {
			wantEvents = append(wantEvents, "create")
		}
		publishedSaveEvents(t, events, wantEvents...)
	}
}
func TestSavePublishedLocalCancellationAndDependencyBoundaries(t *testing.T) {
	for _, stage := range []int{-2, -1, 1, 2, 3} {
		in, d, s, c, events := publishedSaveFixture()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cancelWhen := func(yes bool) {
			if yes {
				cancel()
			}
		}
		s.onLookup = func() { cancelWhen(stage == -1) }
		c.onRead = func(n int) { cancelWhen(n == stage) }
		d.NextID = func(context.Context) (int64, error) {
			*events = append(*events, "id")
			cancelWhen(stage == 3)
			return 73, nil
		}
		if stage == -2 {
			cancel()
			d = LocalFileSaveDependencies{}
		}
		got, err := SavePublishedLocalFile(ctx, in, d)
		publishedSaveError(t, got, err, context.Canceled)
		publishedSaveRequire(t, s.written.Metadata == nil, "cancellation must prevent insertion")
		if stage == -2 {
			publishedSaveEvents(t, events)
		}
		if stage == -1 {
			publishedSaveEvents(t, events, "lookup")
		}
		if stage == 1 || stage == 2 {
			publishedSaveRequire(t, !strings.Contains(strings.Join(*events, ","), "id"), "clock cancellation must stop allocation")
		}
		if stage == 3 {
			publishedSaveEvents(t, events, "lookup", "clock", "clock", "id")
		}
	}
	for _, tc := range []struct {
		name   string
		remove func(*LocalFileSaveDependencies)
	}{
		{"store", func(d *LocalFileSaveDependencies) { d.Store = nil }}, {"allocator", func(d *LocalFileSaveDependencies) { d.NextID = nil }}, {"clock", func(d *LocalFileSaveDependencies) { d.Clock = nil }},
	} {
		in, d, _, _, events := publishedSaveFixture()
		tc.remove(&d)
		got, err := SavePublishedLocalFile(context.Background(), in, d)
		publishedSaveError(t, got, err, ErrNilLocalFileSaveDependency)
		publishedSaveEvents(t, events)
	}
}

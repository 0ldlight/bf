package localrepo

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/repository"
)

// 真实 provider/Add/节点命令，只有事务端口驻内存；不代表 PostgreSQL 验收。
// Real provider/Add/node command with an in-memory transaction port, not PostgreSQL acceptance.
type streamProvider struct {
	store    *filestore.Store
	events   *[]string
	expected filestore.Expected
	calls    int
	fail     error
}

func (p *streamProvider) Put(ctx context.Context, r io.Reader, e filestore.Expected) (content.Metadata, error) {
	*p.events = append(*p.events, "put")
	p.calls++
	p.expected = e
	if p.fail != nil {
		return content.Metadata{}, p.fail
	}
	return p.store.Put(ctx, r, e)
}

type streamRegistry struct {
	events           *[]string
	received, winner content.Metadata
	calls            int
	fail             error
}

func (r *streamRegistry) Register(_ context.Context, m content.Metadata) (content.Metadata, error) {
	*r.events = append(*r.events, "register")
	r.calls++
	r.received = m
	if r.fail != nil {
		return content.Metadata{}, r.fail
	}
	if r.winner != (content.Metadata{}) {
		return r.winner, nil
	}
	return m, nil
}

type streamNodeStore struct {
	events              *[]string
	source              repository.FileSnapshotSource
	query               repository.Path
	written             repository.FileReference
	lookupErr, writeErr error
	count               int64
	clockCalls          int
}

func (s *streamNodeStore) LookupSnapshotSource(_ context.Context, p repository.Path) (repository.FileSnapshotSource, error) {
	*s.events = append(*s.events, "lookup")
	s.query = p
	return s.source, s.lookupErr
}
func (s *streamNodeStore) Create(_ context.Context, r repository.FileReference) (int64, error) {
	return s.write("create", r)
}
func (s *streamNodeStore) Update(_ context.Context, r repository.FileReference) (int64, error) {
	return s.write("update", r)
}
func (s *streamNodeStore) write(op string, r repository.FileReference) (int64, error) {
	*s.events = append(*s.events, op)
	s.written = r
	return s.count, s.writeErr
}
func (s *streamNodeStore) NowMillis() int64 {
	*s.events = append(*s.events, "clock")
	s.clockCalls++
	return -int64(s.clockCalls)
}

type streamReader struct {
	io.Reader
	reads  int
	closed bool
	fail   error
}

func (r *streamReader) Read(p []byte) (int, error) {
	r.reads++
	if r.fail != nil {
		return 0, r.fail
	}
	return r.Reader.Read(p)
}
func (r *streamReader) Close() error { r.closed = true; return nil }

type streamCause struct{}

func (*streamCause) Error() string  { return "private-marker" }
func streamUnits(s string) []uint16 { return utf16.Encode([]rune(s)) }
func streamFixture(t *testing.T) (FileInput, FileDependencies, *streamProvider, *streamRegistry, *streamNodeStore, *streamReader) {
	t.Helper()
	events := new([]string)
	store, err := filestore.New(t.TempDir())
	streamRequire(t, err == nil, "error=%v", err)
	t.Cleanup(func() { _ = store.Close() })
	p := &streamProvider{store: store, events: events}
	r := &streamRegistry{events: events}
	s := &streamNodeStore{events: events, lookupErr: sql.ErrNoRows, count: 1}
	publisher, err := binary.New(p, r)
	streamRequire(t, err == nil, "error=%v", err)
	d := FileDependencies{Publisher: publisher, Node: repository.LocalFileSaveDependencies{Store: s, Clock: s, NextID: func(context.Context) (int64, error) { *events = append(*events, "id"); return 73, nil }}}
	in := FileInput{Repo: "rk", Path: "dir/README", Precheck: SkipChecksumPrecheck, ChecksumPolicy: repository.LocalFileChecksumServer, CurrentUserUTF16: []uint16{'u'}, ResourceCreatedMillis: -7, ResourceModifiedMillis: 44}
	return in, d, p, r, s, &streamReader{Reader: strings.NewReader("abc")}
}
func streamRequire(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}
func streamError(t *testing.T, got FileResult, err, cause error, text string) {
	t.Helper()
	streamRequire(t, got == (FileResult{}) && err != nil && errors.Is(err, cause) && err.Error() == text, "result=%v error=%v; want zero, cause=%v, text=%q", got, err, cause, text)
}
func streamEvents(t *testing.T, p *streamProvider, want ...string) {
	t.Helper()
	streamRequire(t, reflect.DeepEqual(*p.events, want), "events=%v want=%v", *p.events, want)
}
func streamBytesRemain(t *testing.T, p *streamProvider, r *streamRegistry) {
	t.Helper()
	reader, err := p.store.Open(context.Background(), r.received.SHA1)
	streamRequire(t, err == nil, "error=%v", err)
	defer reader.Close()
	b, err := io.ReadAll(reader)
	streamRequire(t, err == nil && string(b) == "abc", "persisted=%q err=%v", b, err)
}
func TestSaveFileCanonicalWinnerAndFreshRawFacts(t *testing.T) {
	in, d, p, r, s, reader := streamFixture(t)
	r.winner = content.Metadata{SHA1: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64), MD5: strings.Repeat("c", 32), Size: 99}
	in.Checksums = repository.ClientFileChecksums{SHA1Original: []uint16{'X', 0xd800}, MD5Original: []uint16{}}
	in.CurrentUserUTF16 = []uint16{'u', 0xdc00}
	actorBefore := slices.Clone(in.CurrentUserUTF16)
	in.CreatedPolicy, in.OriginalCase = 0, 255
	before := append([]uint16(nil), in.Checksums.SHA1Original...)
	got, err := SaveFile(context.Background(), reader, in, d)
	streamRequire(t, err == nil && got.Published == r.winner && got.Save == (repository.LocalFileSaveResult{ID: 73, Created: true, Changed: true, Count: 1}), "result=%v error=%v", got, err)
	m := s.written.Metadata
	streamRequire(t, r.received != r.winner && s.written.Content == r.winner && s.written.Path == s.query && s.written.ID == 73 && m != nil && m.CreatedMillis() == -7 && m.ModifiedMillis() == 44 && m.UpdatedMillis() == -2 && reflect.DeepEqual(m.CreatedBy().UTF16(), actorBefore) && reflect.DeepEqual(m.ModifiedBy().UTF16(), actorBefore) && reflect.DeepEqual(m.SHA1OriginalUTF16(), before) && reflect.DeepEqual(m.MD5OriginalUTF16(), []uint16{}), "canonical content or raw node facts lost")
	streamRequire(t, reflect.DeepEqual(in.Checksums.SHA1Original, before) && reflect.DeepEqual(in.CurrentUserUTF16, actorBefore) && !reader.closed && reader.reads != 0, "input ownership violated")
	streamEvents(t, p, "put", "register", "lookup", "clock", "clock", "id", "create")
}
func TestSaveFileLoadedUpdateKeepsStoredIdentityAndZeroCount(t *testing.T) {
	for _, count := range []int64{0, 1} {
		in, d, p, _, s, reader := streamFixture(t)
		s.count = count
		s.lookupErr = nil
		path, err := repository.NewPath("stored", "other", "README", true)
		streamRequire(t, err == nil, "error=%v", err)
		m := repository.NewFileMetadata(repository.FileMetadataInput{CreatedMillis: 11, ModifiedMillis: 22, UpdatedMillis: 33, CreatedBy: repository.NewActor(streamUnits("creator"))})
		s.source = repository.FileSnapshotSource{Reference: repository.FileReference{ID: 93, Path: path, Metadata: &m, Content: content.Metadata{SHA1: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64), MD5: strings.Repeat("c", 32), Size: 7}}, SHA1ActualValid: true, SHA256ActualValid: true, MD5ActualValid: true}
		in.CreatedPolicy = repository.PreserveLoadedFileCreated
		got, err := SaveFile(context.Background(), reader, in, d)
		streamRequire(t, err == nil && got.Save == (repository.LocalFileSaveResult{ID: 93, Changed: true, Count: count}) && s.written.Path == path && s.written.Content == got.Published && s.query.Repo() == "rk" && s.written.Metadata.CreatedMillis() == 11, "result=%v err=%v written=%v", got, err, s.written)
		streamEvents(t, p, "put", "register", "lookup", "clock", "update")
	}
}
func TestSaveFileVerifyUsesExistingNormalization(t *testing.T) {
	for _, checks := range []repository.ClientFileChecksums{
		{SHA1Original: streamUnits("A9993E364706816ABA3E25717850C26C9CD0D89D"), SHA256Original: streamUnits("BA7816BF8F01CFEA414140DE5DAE2223B00361A396177A9CB410FF61F20015AD"), MD5Original: streamUnits("900150983CD24FB0D6963F7D28E17F72")},
		{SHA1Original: streamUnits("\t\u2003\u2028"), SHA256Original: streamUnits("NO_ORIG"), MD5Original: []uint16{}}, {},
		{SHA1Original: streamUnits("NO_ORIG"), SHA256Original: streamUnits("NO_ORIG"), MD5Original: streamUnits("NO_ORIG")},
	} {
		in, d, _, _, s, reader := streamFixture(t)
		in.Precheck = VerifyChecksumPrecheck
		in.Checksums = checks
		before := repository.ClientFileChecksums{SHA1Original: slices.Clone(checks.SHA1Original), SHA256Original: slices.Clone(checks.SHA256Original), MD5Original: slices.Clone(checks.MD5Original)}
		got, err := SaveFile(context.Background(), reader, in, d)
		streamRequire(t, err == nil && got.Save.Created && !reader.closed, "result=%v err=%v", got, err)
		md5Want := before.MD5Original
		if len(md5Want) == 32 {
			md5Want = streamUnits("900150983cd24fb0d6963f7d28e17f72")
		}
		want := before.SHA1Original
		if len(want) == 40 {
			want = streamUnits(strings.ToLower(string(utf16.Decode(want))))
		}
		streamRequire(t, reflect.DeepEqual(s.written.Metadata.MD5OriginalUTF16(), md5Want) && reflect.DeepEqual(s.written.Metadata.SHA1OriginalUTF16(), want) && reflect.DeepEqual(in.Checksums, before), "precheck mutated raw Originals or node normalization")
	}
}
func TestSaveFileExplicitPrecheckIndependentOfNodePolicy(t *testing.T) {
	for _, mode := range []ChecksumPrecheck{SkipChecksumPrecheck, VerifyChecksumPrecheck} {
		for _, policy := range []repository.LocalFileChecksumPolicy{repository.LocalFileChecksumClient, repository.LocalFileChecksumServer} {
			in, d, p, r, s, reader := streamFixture(t)
			in.Precheck, in.ChecksumPolicy = mode, policy
			in.Checksums.MD5Original = streamUnits(strings.Repeat("0", 32))
			got, err := SaveFile(context.Background(), reader, in, d)
			switch {
			case mode == VerifyChecksumPrecheck:
				streamError(t, got, err, filestore.ErrChecksumMismatch, "localrepo: file publication failed")
				streamEvents(t, p, "put")
			case policy == repository.LocalFileChecksumClient:
				streamError(t, got, err, repository.ErrClientChecksumMismatch, "localrepo: file node save failed")
				streamEvents(t, p, "put", "register", "lookup", "clock")
				streamBytesRemain(t, p, r)
			default:
				streamRequire(t, err == nil && got.Save.Created && reflect.DeepEqual(s.written.Metadata.MD5OriginalUTF16(), in.Checksums.MD5Original), "result=%v err=%v", got, err)
			}
			streamRequire(t, !reader.closed && p.calls == 1, "reader closed or publication retried")
		}
	}
}
func TestSaveFileMalformedUTF16IsEarlyOnlyInVerify(t *testing.T) {
	for _, raw := range [][]uint16{{0xd800}, {0xdc00}, {0xdc00, 0xd800}} {
		for field := 0; field < 3; field++ {
			for _, mode := range []ChecksumPrecheck{SkipChecksumPrecheck, VerifyChecksumPrecheck} {
				in, d, p, _, s, reader := streamFixture(t)
				in.Precheck = mode
				slots := []*[]uint16{&in.Checksums.SHA1Original, &in.Checksums.SHA256Original, &in.Checksums.MD5Original}
				*slots[field] = raw
				got, err := SaveFile(context.Background(), reader, in, d)
				if mode == VerifyChecksumPrecheck {
					streamError(t, got, err, ErrUnsupportedOriginalEncoding, ErrUnsupportedOriginalEncoding.Error())
					streamEvents(t, p)
					streamRequire(t, reader.reads == 0, "encoding guard read body")
				} else if err != nil || s.written.Metadata == nil {
					t.Fatalf("Skip result=%v err=%v", got, err)
				}
				streamRequire(t, reflect.DeepEqual(*slots[field], raw) && !reader.closed, "raw input changed or reader closed")
			}
		}
	}
	for _, raw := range []string{"😀", "no_orig", "\u00a0", "\u0085"} {
		in, d, p, _, _, reader := streamFixture(t)
		in.Precheck, in.Checksums.SHA1Original = VerifyChecksumPrecheck, streamUnits(raw)
		got, err := SaveFile(context.Background(), reader, in, d)
		streamError(t, got, err, filestore.ErrChecksumMismatch, "localrepo: file publication failed")
		streamEvents(t, p, "put")
		streamRequire(t, reader.reads > 0, "valid UTF16/nonblank Original must reach provider comparison")
	}
}
func TestSaveFileDeclaredSizeBothModesReadsBeforeRejecting(t *testing.T) {
	for _, mode := range []ChecksumPrecheck{SkipChecksumPrecheck, VerifyChecksumPrecheck} {
		for _, size := range []int64{-1, 2, 3} {
			in, d, p, _, _, reader := streamFixture(t)
			in.Precheck, in.DeclaredSize = mode, &size
			if size != 3 {
				in.Checksums.SHA1Original = streamUnits("bad-digest")
			}
			got, err := SaveFile(context.Background(), reader, in, d)
			streamRequire(t, p.expected.Size != nil && *p.expected.Size == size && p.expected.Size != in.DeclaredSize && reader.reads != 0 && !reader.closed, "size must be copied and validated after reading")
			if size == 3 {
				streamRequire(t, err == nil && got.Save.Created, "result=%v err=%v", got, err)
			} else {
				streamError(t, got, err, filestore.ErrLengthMismatch, "localrepo: file publication failed")
				streamEvents(t, p, "put")
			}
		}
	}
}
func TestSaveFileGuardsPrecedeReaderAndHaveFixedErrors(t *testing.T) {
	for stage := 0; stage < 9; stage++ {
		in, d, p, _, _, reader := streamFixture(t)
		ctx := context.Background()
		var input io.Reader = reader
		want, text := ErrNilFileDependency, ErrNilFileDependency.Error()
		switch stage {
		case 0:
			c, cancel := context.WithCancel(ctx)
			cancel()
			ctx, d, input = c, FileDependencies{}, nil
			want, text = context.Canceled, "localrepo: file save cancelled"
		case 1:
			d.Publisher, input, in.Precheck = nil, nil, 0
		case 2:
			d.Node.Store = nil
		case 3:
			d.Node.NextID = nil
		case 4:
			d.Node.Clock = nil
		case 5:
			input, in.Precheck = nil, 0
			want, text = ErrNilFileReader, ErrNilFileReader.Error()
		case 6:
			in.Precheck, in.Checksums.SHA1Original = 0, []uint16{0xd800}
			want, text = ErrUnsupportedChecksumPrecheck, ErrUnsupportedChecksumPrecheck.Error()
		case 8:
			in.Precheck = 255
			want, text = ErrUnsupportedChecksumPrecheck, ErrUnsupportedChecksumPrecheck.Error()
		case 7:
			in.Precheck, in.Checksums.MD5Original = VerifyChecksumPrecheck, []uint16{0xdc00}
			want, text = ErrUnsupportedOriginalEncoding, ErrUnsupportedOriginalEncoding.Error()
		}
		got, err := SaveFile(ctx, input, in, d)
		streamError(t, got, err, want, text)
		if stage != 0 {
			streamRequire(t, err == want, "configuration guard must return sentinel directly")
		}
		streamEvents(t, p)
		streamRequire(t, reader.reads == 0 && !reader.closed, "guard consumed/closed reader")
	}
}
func TestSaveFilePublicationFailuresPreserveCauseAndPublishedBytes(t *testing.T) {
	for stage := 0; stage < 3; stage++ {
		in, d, p, r, _, reader := streamFixture(t)
		cause := &streamCause{}
		*[]*error{&reader.fail, &p.fail, &r.fail}[stage] = cause
		got, err := SaveFile(context.Background(), reader, in, d)
		streamError(t, got, err, cause, "localrepo: file publication failed")
		var typed *streamCause
		streamRequire(t, errors.As(err, &typed) && typed == cause && !reader.closed && p.calls == 1, "original cause/reader ownership/retry contract lost")
		if stage == 2 {
			streamEvents(t, p, "put", "register")
			streamBytesRemain(t, p, r)
		} else {
			streamEvents(t, p, "put")
		}
	}
}
func TestSaveFileNodeFailuresDoNotCompensateOrRetry(t *testing.T) {
	for _, stage := range []string{"path", "folder", "lookup", "id", "create", "policy"} {
		in, d, p, r, s, reader := streamFixture(t)
		cause := error(&streamCause{})
		wantEvents := []string{"put", "register"}
		switch stage {
		case "path":
			in.Path = ""
			cause = repository.ErrInvalidPath
		case "folder":
			path, err := repository.NewPath("rk", "", "folder", false)
			streamRequire(t, err == nil, "error=%v", err)
			s.lookupErr = nil
			s.source.Reference = repository.FileReference{ID: 93, Path: path}
			in.CreatedPolicy = repository.PreserveLoadedFileCreated
			cause = repository.ErrInvalidPath
			wantEvents = append(wantEvents, "lookup")
		case "lookup":
			s.lookupErr = cause
			wantEvents = append(wantEvents, "lookup")
		case "id":
			d.Node.NextID = func(context.Context) (int64, error) { *p.events = append(*p.events, "id"); return 73, cause }
			wantEvents = append(wantEvents, "lookup", "clock", "clock", "id")
		case "policy":
			in.ChecksumPolicy, cause = 0, repository.ErrUnsupportedLocalFileChecksumPolicy
			wantEvents = append(wantEvents, "lookup", "clock")
		case "create":
			s.writeErr = cause
			wantEvents = append(wantEvents, "lookup", "clock", "clock", "id", "create")
		}
		got, err := SaveFile(context.Background(), reader, in, d)
		streamError(t, got, err, cause, "localrepo: file node save failed")
		if stage == "lookup" || stage == "id" || stage == "create" {
			var typed *streamCause
			streamRequire(t, errors.As(err, &typed) && typed == cause, "node wrapper lost typed cause")
		}
		streamEvents(t, p, wantEvents...)
		streamBytesRemain(t, p, r)
		streamRequire(t, p.calls == 1 && r.calls == 1 && !reader.closed, "failure retried or closed reader")
	}
}

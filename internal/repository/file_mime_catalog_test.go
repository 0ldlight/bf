package repository

import (
	"errors"
	"testing"
)

// Expectations come from BIN-183's real reader/table evidence and the approved
// bounded profile. These tests deliberately use only exported production APIs.
func TestFileMimeCatalogValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		defs    []FileMimeDefinition
		profile FileMimeCaseProfile
	}{
		{"zero profile", nil, 0},
		{"unknown profile", nil, FileMimeCaseProfile(255)},
		{"invalid type UTF8", []FileMimeDefinition{{Type: "secret\xff", Extensions: "txt"}}, FileMimeCaseRootASCII},
		{"invalid extensions UTF8", []FileMimeDefinition{{Type: "text/plain", Extensions: "secret\xff"}}, FileMimeCaseRootASCII},
		{"duplicate type disjoint keys", []FileMimeDefinition{{Type: "same", Extensions: "a"}, {Type: "same", Extensions: "b"}}, FileMimeCaseRootASCII},
		{"duplicate identical entry", []FileMimeDefinition{{Type: "same", Extensions: "a"}, {Type: "same", Extensions: "a"}}, FileMimeCaseRootASCII},
		{"duplicate empty type", []FileMimeDefinition{{Type: "", Extensions: "a"}, {Type: "", Extensions: "b"}}, FileMimeCaseRootASCII},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewFileMimeCatalog(tc.defs, tc.profile)
			if !errors.Is(err, ErrInvalidFileMimeCatalog) || err.Error() != "repository: invalid file mime catalog" {
				t.Fatalf("constructor error = %v", err)
			}
			_, err = MaterializeFileMime(mimeSnapshot(t, mimeUnits("a.txt"), nil), got)
			if !errors.Is(err, ErrInvalidFileMimeCatalog) {
				t.Fatalf("failed constructor returned usable catalog: %v", err)
			}
		})
	}
}

func TestFileMimeCatalogTokenFacts(t *testing.T) {
	cat := mimeCatalog(t, []FileMimeDefinition{
		{Type: " Text/Raw \t", Extensions: " ,\talpha\r, alpha,\x00beta\x1f,\u2003,\u00a0,\u0085,\u2007,\u202f,\u00a0wrapped\u00a0, UPPER, \\odd, a/b, xml:properties"},
		{Type: "text/x-swift ", Extensions: "swift"},
		{Type: "", Extensions: "empty"},
		{Type: " ", Extensions: "space"},
		{Type: "text/raw", Extensions: "distinct"},
	}, FileMimeCaseRootASCII)
	for _, tc := range []struct{ path, want string }{
		{"a.alpha", " Text/Raw \t"}, {"a.beta", " Text/Raw \t"},
		{"a.UPPER", "application/octet-stream"}, {"a.swift", "text/x-swift "},
		{"a.empty", ""}, {"a.space", " "}, {"a.distinct", "text/raw"},
		{"a.\\odd", " Text/Raw \t"}, {"a.a/b", " Text/Raw \t"},
		{"a.xml:properties", " Text/Raw \t"}, {"a.", "application/octet-stream"},
	} {
		t.Run(tc.path, func(t *testing.T) { mimeWant(t, mimeSnapshot(t, mimeUnits(tc.path), nil), cat, mimeUnits(tc.want)) })
	}
	// Non-ASCII extension queries are excluded. Put those opaque tokens beside
	// ASCII 'i': if they are incorrectly trimmed or treated as blank they would
	// acquire the reachable key 'i', unlike the target adapter's exact keys.
	for _, edge := range []string{"\u00a0", "\u0085", "\u2007", "\u202f"} {
		c := mimeCatalog(t, []FileMimeDefinition{{Type: "opaque", Extensions: edge + "i" + edge}}, FileMimeCaseRootASCII)
		mimeWant(t, mimeSnapshot(t, mimeUnits("a.i"), nil), c, mimeUnits("application/octet-stream"))
	}
	// EM SPACE is Java blank as a whole token; a nonblank token with em-space
	// edges is not Java-trimmed, so it must not become the ASCII key 'i'.
	c := mimeCatalog(t, []FileMimeDefinition{{Type: "opaque", Extensions: "\u2003,\u2003i\u2003"}}, FileMimeCaseRootASCII)
	mimeWant(t, mimeSnapshot(t, mimeUnits("a.i"), nil), c, mimeUnits("application/octet-stream"))
}

func TestFileMimeCatalogPermanentAmbiguity(t *testing.T) {
	cat := mimeCatalog(t, []FileMimeDefinition{
		{Type: "text/xsl", Extensions: "xsl, left, left"},
		{Type: "application/xml", Extensions: "xsl, xml"},
		{Type: "third/type", Extensions: "xsl, right"},
	}, FileMimeCaseRootASCII)
	for _, path := range []string{"a.xsl", "a.XSL"} {
		mimeError(t, mimeSnapshot(t, mimeUnits(path), nil), cat, ErrAmbiguousFileMimeExtension)
	}
	for _, tc := range []struct{ path, want string }{
		{"a.left", "text/xsl"}, {"a.xml", "application/xml"}, {"a.right", "third/type"},
		{"a.missing", "application/octet-stream"},
	} {
		mimeWant(t, mimeSnapshot(t, mimeUnits(tc.path), nil), cat, mimeUnits(tc.want))
	}
}

func TestFileMimeCatalogCaseProfiles(t *testing.T) {
	defs := []FileMimeDefinition{
		{Type: "ascii-i", Extensions: "i"}, {Type: "dotless-i", Extensions: "ı"},
		{Type: "upper-key", Extensions: "TXT"}, {Type: "text/plain", Extensions: "txt"},
	}
	for _, tc := range []struct {
		name    string
		profile FileMimeCaseProfile
		want    string
	}{
		{"ROOT", FileMimeCaseRootASCII, "ascii-i"},
		{"Turkic", FileMimeCaseTurkicASCII, "dotless-i"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := mimeCatalog(t, defs, tc.profile)
			mimeWant(t, mimeSnapshot(t, mimeUnits("a.I"), nil), cat, mimeUnits(tc.want))
			mimeWant(t, mimeSnapshot(t, mimeUnits("a.TXT"), nil), cat, mimeUnits("text/plain"))
			for _, path := range []string{"a.İ", "a.ı", "a.é"} {
				mimeError(t, mimeSnapshot(t, mimeUnits(path), nil), cat, ErrUnsupportedFileMimeExtension)
			}
		})
	}
	upperOnly := mimeCatalog(t, defs[2:3], FileMimeCaseRootASCII)
	mimeWant(t, mimeSnapshot(t, mimeUnits("a.TXT"), nil), upperOnly, mimeUnits("application/octet-stream"))
}

func TestFileMimeCatalogEmptyAndDefinitionOwnership(t *testing.T) {
	for _, defs := range [][]FileMimeDefinition{nil, {}} {
		for _, profile := range []FileMimeCaseProfile{FileMimeCaseRootASCII, FileMimeCaseTurkicASCII} {
			cat := mimeCatalog(t, defs, profile)
			mimeWant(t, mimeSnapshot(t, mimeUnits("a.txt"), nil), cat, mimeUnits("application/octet-stream"))
		}
	}
	defs := []FileMimeDefinition{{Type: "text/plain", Extensions: "txt"}}
	cat := mimeCatalog(t, defs, FileMimeCaseRootASCII)
	copyCat := cat
	defs[0] = FileMimeDefinition{Type: "changed", Extensions: "changed"}
	defs = append(defs, FileMimeDefinition{Type: "another", Extensions: "txt"})
	for _, c := range []FileMimeCatalog{cat, copyCat} {
		mimeWant(t, mimeSnapshot(t, mimeUnits("a.txt"), nil), c, mimeUnits("text/plain"))
		mimeWant(t, mimeSnapshot(t, mimeUnits("a.changed"), nil), c, mimeUnits("application/octet-stream"))
	}
}

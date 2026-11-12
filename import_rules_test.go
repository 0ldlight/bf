// internal 依赖方向的永久契约。
// A permanent contract for internal dependency direction.
//
// docs/ENGINEERING-MAP.md documents the verified import graph in prose;
// this test is its executable source of truth. Every change to an internal
// import edge must update the allowlist below and the engineering map in
// the same change, so the map can no longer drift silently.
// docs/ENGINEERING-MAP.md 以文字记录经证实的 import 图;本测试是其可执行
// 权威。任何内部依赖边的变化必须同时更新下方白名单与工程地图,地图不再
// 无声漂移。
//
// Only production imports are governed: a package's tests may import any
// internal package for verification. The separate integration/postgres
// module is outside this contract.
// 仅约束生产 import:包的测试可为验证导入任意内部包。独立的
// integration/postgres 模块不在本契约内。
package bf_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const internalPrefix = "github.com/0ldlight/bf/internal/"

// allowedInternalImports freezes the verified graph (edge set must match
// exactly in both directions: new edges and removed edges both fail).
// allowedInternalImports 冻结经证实的图(边集双向精确匹配:新增边与移除边
// 都会失败)。
var allowedInternalImports = map[string][]string{
	"github.com/0ldlight/bf/cmd/binflow":                  {"internal/process"},
	"github.com/0ldlight/bf/cmd/binflow-contract":         {"internal/compatprobe"},
	"github.com/0ldlight/bf/internal/binary":              {"internal/content", "internal/filestore"},
	"github.com/0ldlight/bf/internal/compatprobe":         {},
	"github.com/0ldlight/bf/internal/content":             {},
	"github.com/0ldlight/bf/internal/filestore":           {"internal/content"},
	"github.com/0ldlight/bf/internal/httpserver":          {"internal/filestore", "internal/localrepo", "internal/repository"},
	"github.com/0ldlight/bf/internal/localrepo/postgres":  {"internal/binary", "internal/localrepo", "internal/repository", "internal/repository/postgres", "internal/security"},
	"github.com/0ldlight/bf/internal/localrepo":           {"internal/binary", "internal/content", "internal/filestore", "internal/repository"},
	"github.com/0ldlight/bf/internal/process":             {"internal/binary", "internal/filestore", "internal/httpserver", "internal/localrepo/postgres", "internal/registry/postgres", "internal/repository/postgres", "internal/security"},
	"github.com/0ldlight/bf/internal/registry/postgres":   {"internal/content"},
	"github.com/0ldlight/bf/internal/repository/postgres": {"internal/content", "internal/repository"},
	"github.com/0ldlight/bf/internal/repository":          {"internal/content"},
	"github.com/0ldlight/bf/internal/security":            {"internal/httpserver", "internal/localrepo", "internal/repository", "internal/repository/postgres"},
}

type listedPackage struct {
	ImportPath string
	Imports    []string
}

func listInternalGraph(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./internal/...", "./cmd/...")
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list failed: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("go list failed: %v", err)
	}
	graph := make(map[string][]string)
	dec := json.NewDecoder(strings.NewReader(string(stdout)))
	for dec.More() {
		var pkg listedPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		internal := make([]string, 0, len(pkg.Imports))
		for _, imp := range pkg.Imports {
			if strings.HasPrefix(imp, internalPrefix) {
				internal = append(internal, strings.TrimPrefix(imp, "github.com/0ldlight/bf/"))
			}
		}
		graph[pkg.ImportPath] = internal
	}
	if len(graph) == 0 {
		t.Fatal("go list returned no packages")
	}
	return graph
}

func TestInternalImportGraphMatchesContract(t *testing.T) {
	graph := listInternalGraph(t)

	for path, actual := range graph {
		allowed, known := allowedInternalImports[path]
		if !known {
			t.Errorf(
				"unknown package %s: add it to allowedInternalImports and to the module table in docs/ENGINEERING-MAP.md in the same change",
				path,
			)
			continue
		}
		if !reflect.DeepEqual(normalize(actual), normalize(allowed)) {
			t.Errorf(
				"import contract mismatch for %s:\n  actual: %v\n  allowlist: %v\nUpdate allowedInternalImports and the dependency section of docs/ENGINEERING-MAP.md together",
				path, normalize(actual), normalize(allowed),
			)
		}
	}
	for path := range allowedInternalImports {
		if _, listed := graph[path]; !listed {
			t.Errorf(
				"allowlist references %s which go list no longer returns: remove the stale entry and update docs/ENGINEERING-MAP.md",
				path,
			)
		}
	}
}

func normalize(deps []string) []string {
	out := append([]string(nil), deps...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func TestAllowlistEntriesAreWellFormed(t *testing.T) {
	for path, deps := range allowedInternalImports {
		if !strings.HasPrefix(path, "github.com/0ldlight/bf/") {
			t.Errorf("allowlist key %s is outside the module", path)
		}
		seen := make(map[string]bool)
		for _, dep := range deps {
			if !strings.HasPrefix(dep, "internal/") {
				t.Errorf("allowlist for %s contains non-internal dependency %q", path, dep)
			}
			if dep == "internal/" {
				t.Errorf("allowlist for %s contains malformed entry %q", path, dep)
			}
			if seen[dep] {
				t.Errorf("allowlist for %s lists %q twice", path, dep)
			}
			seen[dep] = true
		}
		if _, err := filepath.Match(path, path); err != nil {
			t.Errorf("allowlist key %s is not a literal path", path)
		}
	}
}

package architecture

// The repository contract suite (internal/adapters/outbound/repocontract) is
// test support: it imports "testing" and builds fixtures with a fixed clock.
// Its files are not _test.go files so that both the memory doubles and the
// Postgres adapters can run it, which is exactly why the shipped binary must
// never link it.

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const repocontractSuffix = "/internal/adapters/outbound/repocontract"

// repocontractImports returns one finding per import of the contract suite
// (or a package below it) in src.
func repocontractImports(filename string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(path, repocontractSuffix) || strings.Contains(path, repocontractSuffix+"/") {
			out = append(out, fmt.Sprintf("%s imports %s", fset.Position(imp.Pos()), path))
		}
	}
	return out, nil
}

func TestRuntimeBinaryDoesNotImportTheRepositoryContract(t *testing.T) {
	cmdDir := filepath.Join("..", "..", "cmd")
	if _, err := os.Stat(cmdDir); os.IsNotExist(err) {
		t.Skip("no cmd/ yet: this sensor starts enforcing with the first binary")
	}
	files := goFilesUnder(t, cmdDir, false)
	if len(files) == 0 {
		t.Fatal("cmd/ exists but holds no non-test Go file: the sensor would pass vacuously")
	}
	sort.Strings(files)
	for _, path := range files {
		findings, err := repocontractImports(path, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, finding := range findings {
			t.Errorf("ARCHITECTURE VIOLATION: cmd/** must never import the repository contract suite (test support)\n  WHAT: %s", finding)
		}
	}
}

func TestRepositoryContractSensorBites(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"plain import is flagged", "package main\nimport \"example.com/svc/internal/adapters/outbound/repocontract\"", 1},
		{"blank import is flagged", "package main\nimport _ \"example.com/svc/internal/adapters/outbound/repocontract\"", 1},
		{"a package below it is flagged", "package main\nimport \"example.com/svc/internal/adapters/outbound/repocontract/x\"", 1},
		{"the memory doubles are not this sensor's business", "package main\nimport \"example.com/svc/internal/adapters/outbound/memory\"", 0},
		{"a look-alike path passes", "package main\nimport \"example.com/svc/internal/adapters/outbound/repocontracts\"", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repocontractImports("src.go", tc.src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("findings = %v, want %d", got, tc.want)
			}
		})
	}
}

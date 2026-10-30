package architecture

// A2A inbound adapter fitness (docs/adr/0010-a2a-inbound-adapter.md). The A2A
// agent is a third driving adapter over the READ use cases, exactly as the MCP
// server is (ADR 0007):
//
//   - TestA2AAdapterDependencyRule: it depends only on application + domain,
//     and nothing else in internal/ depends on it.
//   - TestA2AAdapterIsReadOnly: its Deps holds no write use case, and its
//     sources reference no use case outside the read allowlist.
//   - TestA2ABinaryIsReadOnlyAndKafkaFree: cmd/a2a wires only the read use
//     cases and imports no Kafka, outbox or CloudEvents package (the relay and
//     every publisher stay in cmd/api).
//   - TestNoOutboundA2AClient: nothing imports the SDK's a2aclient outside of
//     tests; this context calls no sibling.
//   - TestA2ASDKOnlyInTheA2AAdapterAndBinary: the SDK is imported by the
//     adapter and its composition root only.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	archgo "github.com/arch-go/arch-go/api"
	"github.com/arch-go/arch-go/api/configuration"
)

const (
	a2aAdapterDir = "../adapters/inbound/a2a"
	a2aBinaryDir  = "../../cmd/a2a"
	a2aSDKPrefix  = "github.com/a2aproject/a2a-go"
)

// a2aReadUseCases is the whole use-case surface the A2A agent may touch: the
// three reads behind its two skills. Adding to it is a reviewed act (an
// amendment of ADR 0010), and only a READ may be added while the MCP charter
// is read-only (ADR 0007).
var a2aReadUseCases = map[string]bool{
	"GetCatalogItem": true, "NewGetCatalogItem": true,
	"ListListings": true, "NewListListings": true, "ListListingsQuery": true,
	"GetInventoryFeed": true, "NewGetInventoryFeed": true,
	"PageQuery": true,
}

func TestA2AAdapterDependencyRule(t *testing.T) {
	moduleInfo := configuration.Load(modulePath)

	t.Run("a2a adapter depends only on application and domain", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{{
				Package: "**.internal.adapters.inbound.a2a.**",
				ShouldOnlyDependsOn: &configuration.Dependencies{Internal: []string{
					"**.internal.adapters.inbound.a2a.**",
					"**.internal.application.**",
					"**.internal.domain.**",
				}},
			}},
		})
		assertArchGoPasses(t, result)
	})

	t.Run("nothing else depends on the a2a adapter", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{{
				Package:            "**.internal.**",
				ShouldNotDependsOn: &configuration.Dependencies{Internal: []string{"**.internal.adapters.inbound.a2a.**"}},
			}},
		})
		assertArchGoPasses(t, result)
	})
}

// importsOf returns the unquoted import paths of one Go file.
func importsOf(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("%s: bad import %s", path, imp.Path.Value)
		}
		out = append(out, p)
	}
	return out
}

// usecaseRefs returns every identifier X referenced as usecases.X in one file.
func usecaseRefs(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "usecases" {
				seen[sel.Sel.Name] = true
			}
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// assertOnlyReadUseCases fails for every usecases.X referenced under dir that
// is not on the read allowlist.
func assertOnlyReadUseCases(t *testing.T, dir string) {
	t.Helper()
	files := goFilesUnder(t, dir, false)
	if len(files) == 0 {
		t.Fatalf("no Go sources under %s: the sensor would pass vacuously", dir)
	}
	for _, path := range files {
		for _, name := range usecaseRefs(t, path) {
			if !a2aReadUseCases[name] {
				t.Errorf("%s references usecases.%s, which is not a read use case of ADR 0010's allowlist: the A2A agent is read-only because the MCP surface is (ADR 0007)", path, name)
			}
		}
	}
}

func TestA2AAdapterIsReadOnly(t *testing.T) {
	assertOnlyReadUseCases(t, a2aAdapterDir)

	// Deps is the injection seam: every field must be one of the read use cases.
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(a2aAdapterDir, "server.go"), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	fields := 0
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "Deps" {
			return true
		}
		st, ok := spec.Type.(*ast.StructType)
		if !ok {
			t.Fatal("Deps is not a struct")
		}
		for _, field := range st.Fields.List {
			fields++
			star, ok := field.Type.(*ast.StarExpr)
			if !ok {
				t.Errorf("Deps field %v is not a *usecases.X", field.Names)
				continue
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok || !a2aReadUseCases[sel.Sel.Name] {
				t.Errorf("Deps field %v is not a read use case", field.Names)
			}
		}
		return false
	})
	if fields == 0 {
		t.Fatal("Deps not found or empty in server.go")
	}
}

// writeSidePackages are the import-path tails the read-only binary and
// adapter must never reach: the event publishers, the outbox and the envelope.
var writeSidePackages = []string{
	"/internal/adapters/outbound/kafka", "/internal/adapters/outbound/outbox", "/internal/adapters/inbound/kafka",
	"/internal/adapters/kafka/cloudevents", "github.com/segmentio/kafka-go",
}

func TestA2ABinaryIsReadOnlyAndKafkaFree(t *testing.T) {
	assertOnlyReadUseCases(t, a2aBinaryDir)
	for _, dir := range []string{a2aBinaryDir, a2aAdapterDir} {
		for _, path := range goFilesUnder(t, dir, false) {
			for _, imp := range importsOf(t, path) {
				for _, banned := range writeSidePackages {
					if strings.HasSuffix(imp, banned) || strings.Contains(imp, banned+"/") {
						t.Errorf("%s imports %s: the A2A agent is read-only, the relay and every publisher stay in cmd/api (ADR 0010)", path, imp)
					}
				}
			}
		}
	}
}

func TestNoOutboundA2AClient(t *testing.T) {
	for _, root := range []string{"..", "../../cmd"} {
		for _, path := range goFilesUnder(t, root, false) {
			for _, imp := range importsOf(t, path) {
				if strings.HasPrefix(imp, a2aSDKPrefix) && strings.HasSuffix(strings.TrimSuffix(imp, "/"), "/a2aclient") {
					t.Errorf("%s imports %s: this context calls no sibling, so it carries no outbound A2A client (ADR 0010)", path, imp)
				}
			}
		}
	}
}

func TestA2ASDKOnlyInTheA2AAdapterAndBinary(t *testing.T) {
	allowed := []string{
		filepath.ToSlash(filepath.Clean(a2aAdapterDir)) + "/",
		filepath.ToSlash(filepath.Clean(a2aBinaryDir)) + "/",
	}
	for _, root := range []string{"..", "../../cmd"} {
		for _, path := range goFilesUnder(t, root, false) {
			slash := filepath.ToSlash(filepath.Clean(path))
			if hasAnyPrefix(slash, allowed) {
				continue
			}
			for _, imp := range importsOf(t, path) {
				if strings.HasPrefix(imp, a2aSDKPrefix) {
					t.Errorf("%s imports the A2A SDK (%s): only the A2A adapter and cmd/a2a may", path, imp)
				}
			}
		}
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

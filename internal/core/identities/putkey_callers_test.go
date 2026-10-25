package identities

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestPutPublicKeyCallers is the gate that Service.registerTx is the only
// non-test caller of Tx.PutPublicKey: every key enters through the register
// rules (admin guard, enrollment token, human refusal). The Store engines
// under internal/store implement the method and are not callers.
func TestPutPublicKeyCallers(t *testing.T) {
	root := filepath.Join("..", "..", "..") // the module root
	fset := token.NewFileSet()
	var callers []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".claude" || rel == filepath.Join("internal", "store") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "PutPublicKey" {
						callers = append(callers, filepath.ToSlash(rel)+" "+fn.Name.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "internal/core/identities/register.go registerTx"
	if len(callers) != 1 || callers[0] != want {
		t.Fatalf("non-test PutPublicKey callers = %q, want exactly [%q]; a new key path must go through registerTx", callers, want)
	}
}

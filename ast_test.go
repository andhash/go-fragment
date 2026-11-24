package fragment

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestNoFloatInMoneyPath(t *testing.T) {
	for _, path := range []string{"breakdown.go", "internal/protocol/invoice.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if ok && (id.Name == "float32" || id.Name == "float64") {
				t.Fatalf("%s contains %s", path, id.Name)
			}
			return true
		})
	}
}

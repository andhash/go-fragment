package fragment

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var addressLiteral = regexp.MustCompile(`^(?:UQ|EQ)[A-Za-z0-9_-]{46}$|^-?[0-9]+:[0-9a-fA-F]{64}$`)

func TestDenylistNotInModule(t *testing.T) {
	if sweepRaw == "" || feeRaw == "" || sweepRaw == feeRaw {
		t.Fatal("denylist declarations were removed")
	}
	if got, err := NormalizeAddress(sweepFriendly); err != nil || got != sweepRaw {
		t.Fatalf("sweep %s %v", got, err)
	}
	if got, err := NormalizeAddress(feeFriendly); err != nil || got != feeRaw {
		t.Fatalf("fee %s %v", got, err)
	}
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || filepath.Base(path) == "denylist.go" {
			if err == nil && d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !addressLiteral.MatchString(s) {
				return true
			}
			raw, err := NormalizeAddress(s)
			if err != nil {
				return true
			}
			if raw == sweepRaw || raw == feeRaw {
				t.Fatalf("%s contains a denylist account outside denylist.go", path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

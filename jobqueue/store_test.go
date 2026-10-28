/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package jobqueue

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const boltImportPath = "go.etcd.io/bbolt"

// boltOnlyFiles are the non-test jobqueue files allowed to reach bbolt and the
// *db type directly (spec A1 acceptance test 1).
var boltOnlyFiles = []string{ //nolint:gochecknoglobals
	"dependency.go", "writeturns.go", "reqstat_cache.go", "archivefold.go", "store_bolt.go",
}

func TestStoreInterface(t *testing.T) {
	Convey("Outside the bbolt files, no jobqueue source imports bbolt or uses the db type", t, func() {
		violations, checked := storeBoundaryViolations(t)

		So(checked, ShouldBeGreaterThan, 0)
		So(violations, ShouldBeEmpty)
	})

	Convey("The boundary check catches each way of using db as a type", t, func() {
		for _, use := range []string{
			"var x *db",
			"func f(s any) { switch s.(type) { case int, *db: } }",
			"func f() { zz[*db]() }",
			"func f() { _ = zz[int, []db] }",
			"func f() { _ = T[*db]{} }",
			"func f(s any) { _ = s.(*db) }",
			"func f() { _ = new(db) }",
			"func f() { _ = (*db)(nil) }",
		} {
			So(snippetStoreBoundaryViolations(t, use), ShouldHaveLength, 1)
		}

		So(snippetStoreBoundaryViolations(t,
			"func f(db []byte) { _ = s.db; _ = m[s.db]; switch x := 1; x { case 1: } }"), ShouldBeEmpty)
	})

	Convey("A rerunGuard keeps a dependent live only once its add's first job is stored", t, func() {
		g := &rerunGuard{archived: make(map[string]bool)}
		g.setDependents([]string{"k1"}, []byte("m"))

		So(g.keeps(func([]byte) bool { return false }, "k1"), ShouldBeFalse)
		So(g.keptLive("k1"), ShouldBeFalse)

		var asked []byte

		stored := func(member []byte) bool {
			asked = member

			return bytes.Equal(member, []byte("m"))
		}

		So(g.keeps(stored, "k1"), ShouldBeTrue)
		So(string(asked), ShouldEqual, "m")
		So(g.keptLive("k1"), ShouldBeTrue)
		So(g.keeps(stored, "k2"), ShouldBeFalse)
		So(g.keptLive("k2"), ShouldBeFalse)
	})
}

// storeBoundaryViolations parses every non-test, non-bbolt .go file in this
// package and returns a description of each bbolt import and each use of the
// identifier db as a type, along with the number of files checked.
func storeBoundaryViolations(t *testing.T) ([]string, int) {
	t.Helper()

	paths, err := filepath.Glob("*.go")
	So(err, ShouldBeNil)

	fset := token.NewFileSet()

	var (
		violations []string
		checked    int
	)

	for _, path := range paths {
		if !storeBoundaryApplies(path) {
			continue
		}

		f, errp := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		So(errp, ShouldBeNil)

		checked++

		violations = append(violations, fileStoreBoundaryViolations(fset, f)...)
	}

	return violations, checked
}

// snippetStoreBoundaryViolations parses src as the body of a jobqueue file and
// returns its boundary violations.
func snippetStoreBoundaryViolations(t *testing.T, src string) []string {
	t.Helper()

	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, "snippet.go", "package jobqueue\n"+src, parser.SkipObjectResolution)
	So(err, ShouldBeNil)

	return fileStoreBoundaryViolations(fset, f)
}

func storeBoundaryApplies(path string) bool {
	if strings.HasSuffix(path, "_test.go") || path == "db.go" || strings.HasPrefix(path, "db_") {
		return false
	}

	return !slices.Contains(boltOnlyFiles, path)
}

func fileStoreBoundaryViolations(fset *token.FileSet, f *ast.File) []string {
	var violations []string

	report := func(pos token.Pos, what string) {
		violations = append(violations, fmt.Sprintf("%s: %s", fset.Position(pos), what))
	}

	for _, imp := range f.Imports {
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && path == boltImportPath {
			report(imp.Pos(), "imports "+boltImportPath)
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		for _, typ := range typePositions(n) {
			if namesDBType(typ) {
				report(typ.Pos(), "uses db as a type")
			}
		}

		return true
	})

	return violations
}

// typePositions returns the expressions of n that are in type position. The
// index of an index expression counts as one, since outside the bbolt files
// db can only be indexed in as a type argument, as in zz[*db]() or T[*db]{}.
func typePositions(n ast.Node) []ast.Expr {
	switch n := n.(type) {
	case *ast.IndexExpr:
		return []ast.Expr{n.Index}
	case *ast.IndexListExpr:
		return n.Indices
	case *ast.TypeSwitchStmt:
		return typeSwitchCases(n)
	case *ast.Field:
		return []ast.Expr{n.Type}
	case *ast.ValueSpec:
		return []ast.Expr{n.Type}
	case *ast.TypeSpec:
		return []ast.Expr{n.Type}
	case *ast.CompositeLit:
		return []ast.Expr{n.Type}
	case *ast.TypeAssertExpr:
		return []ast.Expr{n.Type}
	case *ast.CallExpr:
		return callTypePositions(n)
	}

	return nil
}

// typeSwitchCases returns the types listed in the cases of a type switch.
func typeSwitchCases(ts *ast.TypeSwitchStmt) []ast.Expr {
	var types []ast.Expr

	for _, stmt := range ts.Body.List {
		if clause, ok := stmt.(*ast.CaseClause); ok {
			types = append(types, clause.List...)
		}
	}

	return types
}

// callTypePositions returns the type arguments of new and make, and the type
// of a conversion to a parenthesised type such as (*db)(nil).
func callTypePositions(call *ast.CallExpr) []ast.Expr {
	if paren, ok := call.Fun.(*ast.ParenExpr); ok {
		return []ast.Expr{paren.X}
	}

	if fn, ok := call.Fun.(*ast.Ident); ok && (fn.Name == "new" || fn.Name == "make") && len(call.Args) > 0 {
		return call.Args[:1]
	}

	return nil
}

// namesDBType reports whether the type expression is db, or is built directly
// from it (pointer, slice, array, map, channel or variadic). Function, struct
// and interface types, and type arguments, are not descended into here, since
// their fields and index expressions are visited on their own.
func namesDBType(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "db"
	case *ast.StarExpr:
		return namesDBType(e.X)
	case *ast.ParenExpr:
		return namesDBType(e.X)
	case *ast.ArrayType:
		return namesDBType(e.Elt)
	case *ast.Ellipsis:
		return namesDBType(e.Elt)
	case *ast.ChanType:
		return namesDBType(e.Value)
	case *ast.MapType:
		return namesDBType(e.Key) || namesDBType(e.Value)
	}

	return false
}

// boltDB returns s's store as the bbolt *db, for tests that inspect bbolt
// internals directly.
func (s *Server) boltDB() *db {
	bdb, ok := s.db.(*db)
	if !ok {
		panic("server store is not a *db")
	}

	return bdb
}

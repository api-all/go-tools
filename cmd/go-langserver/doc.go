// Copyright (c) 2016, Zac Bergquist
// Copyright (c) 2017, Dominik Honnef
package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"log"
	"os"

	"golang.org/x/tools/go/ast/astutil"

	"honnef.co/go/tools/loader"
)

const (
	indent    = ""
	preIndent = "    "
)

// Doc holds the resulting documentation for a particular item.
type Doc struct {
	Name   string `json:"name"`
	Import string `json:"import"`
	Pkg    string `json:"pkg"`
	Decl   string `json:"decl"`
	Doc    string `json:"doc"`
	Pos    string `json:"pos"`
}

func (d *Doc) String() string {
	buf := &bytes.Buffer{}
	if d.Import != "" {
		fmt.Fprintf(buf, "import \"%s\"\n\n", d.Import)
	}
	fmt.Fprintf(buf, "%s\n\n", d.Decl)
	if d.Doc == "" {
		d.Doc = "Undocumented."
	}
	doc.ToText(buf, d.Doc, indent, preIndent, 80)
	return buf.String()
}

func builtinPackage() *doc.Package {
	buildPkg, err := build.Import("builtin", "", build.ImportComment)
	// should never fail
	if err != nil {
		panic(err)
	}
	include := func(info os.FileInfo) bool {
		return info.Name() == "builtin.go"
	}
	fs := token.NewFileSet()
	astPkgs, err := parser.ParseDir(fs, buildPkg.Dir, include, parser.ParseComments)
	if err != nil {
		panic(err)
	}
	astPkg := astPkgs["builtin"]
	return doc.New(astPkg, buildPkg.ImportPath, doc.AllDecls)
}

// findInBuiltin searches for an identifier in the builtin package.
// It searches in the following order: funcs, constants and variables,
// and finally types.
func findInBuiltin(name string, obj types.Object, prog *loader.Program) (docstring, decl string) {
	pkg := builtinPackage()

	consts := make([]*doc.Value, 0, 2*len(pkg.Consts))
	vars := make([]*doc.Value, 0, 2*len(pkg.Vars))
	funcs := make([]*doc.Func, 0, 2*len(pkg.Funcs))

	consts = append(consts, pkg.Consts...)
	vars = append(vars, pkg.Vars...)
	funcs = append(funcs, pkg.Funcs...)

	for _, t := range pkg.Types {
		funcs = append(funcs, t.Funcs...)
		consts = append(consts, t.Consts...)
		vars = append(vars, t.Vars...)
	}

	// funcs
	for _, f := range funcs {
		if f.Name == name {
			return f.Doc, formatNode(f.Decl, obj, prog)
		}
	}

	// consts/vars
	for _, v := range consts {
		for _, n := range v.Names {
			if n == name {
				return v.Doc, ""
			}
		}
	}

	for _, v := range vars {
		for _, n := range v.Names {
			if n == name {
				return v.Doc, ""
			}
		}
	}

	// types
	for _, t := range pkg.Types {
		if t.Name == name {
			return t.Doc, formatNode(t.Decl, obj, prog)
		}
	}

	return "", ""
}

// ImportPath gets the import path from an ImportSpec.
func ImportPath(is *ast.ImportSpec) string {
	s := is.Path.Value
	l := len(s)
	// trim the quotation marks
	return s[1 : l-1]
}

// PackageDoc gets the documentation for the package with the specified import
// path and writes it to out.
func PackageDoc(ctxt *build.Context, fset *token.FileSet, srcDir string, importPath string) (*Doc, error) {
	buildPkg, err := ctxt.Import(importPath, srcDir, build.ImportComment)
	if err != nil {
		return nil, err
	}
	// only parse .go files in the specified package
	filter := func(info os.FileInfo) bool {
		for _, fname := range buildPkg.GoFiles {
			if fname == info.Name() {
				return true
			}
		}
		return false
	}
	// TODO we've already parsed the files via go/loader...can we avoid doing it again?
	pkgs, err := parser.ParseDir(fset, buildPkg.Dir, filter, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if astPkg, ok := pkgs[buildPkg.Name]; ok {
		docPkg := doc.New(astPkg, importPath, 0)
		// TODO: we could also include package-level constants, vars, and functions (like the go doc command)
		return &Doc{
			Name:   buildPkg.Name,
			Decl:   "package " + buildPkg.Name,
			Doc:    docPkg.Doc,
			Import: importPath,
			Pkg:    docPkg.Name,
		}, nil
	}
	return nil, errors.New("No documentation found for " + buildPkg.Name)
}

func findTypeSpec(decl *ast.GenDecl, pos token.Pos) *ast.TypeSpec {
	for _, spec := range decl.Specs {
		typeSpec := spec.(*ast.TypeSpec)
		if typeSpec.Pos() == pos {
			return typeSpec
		}
	}
	return nil
}

func findVarSpec(decl *ast.GenDecl, pos token.Pos) *ast.ValueSpec {
	for _, spec := range decl.Specs {
		varSpec := spec.(*ast.ValueSpec)
		for _, ident := range varSpec.Names {
			if ident.Pos() == pos {
				return varSpec
			}
		}
	}
	return nil
}

func formatNode(n ast.Node, obj types.Object, prog *loader.Program) string {
	//fmt.Printf("formatting %T node\n", n)
	var nc ast.Node
	// Render a copy of the node with no documentation.
	// We emit the documentation ourself.
	switch n := n.(type) {
	case *ast.FuncDecl:
		cp := *n
		cp.Doc = nil
		// Don't print the whole function body
		cp.Body = nil
		nc = &cp
	case *ast.Field:
		// Not supported by go/printer

		// TODO(dominikh): Methods in interfaces are syntactically
		// represented as fields. Using types.Object.String for those
		// causes them to look different from real functions.
		// go/printer doesn't include the import paths in names, while
		// Object.String does. Fix that.

		return obj.String()
	case *ast.TypeSpec:
		specCp := *n
		specCp.Doc = nil
		typeSpec := ast.GenDecl{
			Tok:   token.TYPE,
			Specs: []ast.Spec{&specCp},
		}
		nc = &typeSpec
	case *ast.GenDecl:
		cp := *n
		cp.Doc = nil
		if len(n.Specs) > 0 {
			// Only print this one type, not all the types in the gendecl
			switch n.Specs[0].(type) {
			case *ast.TypeSpec:
				spec := findTypeSpec(n, obj.Pos())
				if spec != nil {
					specCp := *spec
					specCp.Doc = nil
					cp.Specs = []ast.Spec{&specCp}
				}
				cp.Lparen = 0
				cp.Rparen = 0
			case *ast.ValueSpec:
				spec := findVarSpec(n, obj.Pos())
				if spec != nil {
					specCp := *spec
					specCp.Doc = nil
					cp.Specs = []ast.Spec{&specCp}
				}
				cp.Lparen = 0
				cp.Rparen = 0
			}
		}
		nc = &cp

	default:
		return obj.String()
	}

	buf := &bytes.Buffer{}
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	err := cfg.Fprint(buf, prog.Fset, nc)
	if err != nil {
		return obj.String()
	}
	return buf.String()
}

// IdentDoc attempts to get the documentation for a *ast.Ident.
func IdentDoc(ctx *build.Context, id *ast.Ident, pkg *loader.Package, prog *loader.Program) (*Doc, error) {
	// get definition of identifier
	obj := pkg.ObjectOf(id)

	// for anonymous fields, we want the type definition, not the field
	if v, ok := obj.(*types.Var); ok && v.Anonymous() {
		obj = pkg.Uses[id]
	}

	var pos string
	if p := obj.Pos(); p.IsValid() {
		pos = prog.Fset.Position(p).String()
	}

	pkgPath, pkgName := "", ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
		pkgName = obj.Pkg().Name()
	}

	// handle packages imported under a different name
	if p, ok := obj.(*types.PkgName); ok {
		return PackageDoc(ctx, prog.Fset, "", p.Imported().Path()) // SRCDIR TODO TODO
	}

	log.Println(obj)
	if obj.Pkg() == nil {
		// special case - builtins
		doc, decl := findInBuiltin(obj.Name(), obj, prog)
		if doc != "" {
			return &Doc{
				Import: "builtin",
				Pkg:    "builtin",
				Name:   obj.Name(),
				Doc:    doc,
				Decl:   decl,
				Pos:    pos,
			}, nil
		}
		return nil, fmt.Errorf("No documentation found for %s", obj.Name())
	}
	log.Println(obj.Pkg())
	log.Println(prog.TypePackages[obj.Pkg()])
	af := prog.TypePackages[obj.Pkg()].Files[prog.Fset.File(obj.Pos())]
	nodes, _ := astutil.PathEnclosingInterval(af, obj.Pos(), obj.Pos())

	var doc *Doc
	for _, node := range nodes {
		switch node.(type) {
		case *ast.Ident:
			// continue ascending AST (searching for parent node of the identifier))
			continue
		case *ast.FuncDecl, *ast.GenDecl, *ast.Field, *ast.TypeSpec, *ast.ValueSpec:
			// found the parent node
		default:
			break
		}
		doc = &Doc{
			Import: pkgPath,
			Pkg:    pkgName,
			Name:   obj.Name(),
			Decl:   formatNode(node, obj, prog),
			Pos:    pos,
		}
		break
	}
	if doc == nil {
		// This shouldn't happen
		return nil, fmt.Errorf("No documentation found for %s", obj.Name())
	}

	for _, node := range nodes {
		//fmt.Printf("for %s: found %T\n%#v\n", id.Name, node, node)
		switch n := node.(type) {
		case *ast.Ident:
			continue
		case *ast.FuncDecl:
			doc.Doc = n.Doc.Text()
			return doc, nil
		case *ast.Field:
			if n.Doc != nil {
				doc.Doc = n.Doc.Text()
			} else if n.Comment != nil {
				doc.Doc = n.Comment.Text()
			}
			return doc, nil
		case *ast.TypeSpec:
			if n.Doc != nil {
				doc.Doc = n.Doc.Text()
				return doc, nil
			}
			if n.Comment != nil {
				doc.Doc = n.Comment.Text()
				return doc, nil
			}
		case *ast.ValueSpec:
			if n.Doc != nil {
				doc.Doc = n.Doc.Text()
				return doc, nil
			}
			if n.Comment != nil {
				doc.Doc = n.Comment.Text()
				return doc, nil
			}
		case *ast.GenDecl:
			constValue := ""
			if c, ok := obj.(*types.Const); ok {
				constValue = c.Val().ExactString()
			}
			if doc.Doc == "" && n.Doc != nil {
				doc.Doc = n.Doc.Text()
			}
			if constValue != "" {
				doc.Doc += fmt.Sprintf("\nConstant Value: %s", constValue)
			}
			return doc, nil
		default:
			return doc, nil
		}
	}
	return doc, nil
}

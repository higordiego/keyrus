// Package bddguard statically rejects vacuous or silently skipped Godog steps.
package bddguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type sourceFile struct {
	path string
	ast  *ast.File
}

type declaration struct {
	body *ast.BlockStmt
}

// ValidateStepSources examines every Go source file, including _test.go. Files
// are analyzed as package groups so handlers declared in another file cannot
// bypass the guard. Nested testdata is excluded from the production scan.
func ValidateStepSources(root string) error {
	groups := make(map[string][]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" && filepath.Clean(path) != filepath.Clean(root) {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".go" {
			groups[filepath.Dir(path)] = append(groups[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	var directories []string
	for directory := range groups {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	for _, directory := range directories {
		sort.Strings(groups[directory])
		if err := validateDirectory(groups[directory]); err != nil {
			return err
		}
	}
	return nil
}

func validateDirectory(paths []string) error {
	set := token.NewFileSet()
	packages := make(map[string][]sourceFile)

	for _, path := range paths {
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse step source %s: %w", path, err)
		}
		packages[file.Name.Name] = append(packages[file.Name.Name], sourceFile{path: path, ast: file})
	}

	var names []string
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validatePackage(set, packages[name]); err != nil {
			return err
		}
	}
	return nil
}

func validatePackage(set *token.FileSet, files []sourceFile) error {
	functions := make(map[string][]declaration)
	methods := make(map[string][]declaration)
	for _, file := range files {
		for _, node := range file.ast.Decls {
			function, ok := node.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			item := declaration{body: function.Body}
			if function.Recv == nil {
				functions[function.Name.Name] = append(functions[function.Name.Name], item)
			} else {
				methods[function.Name.Name] = append(methods[function.Name.Name], item)
			}
		}
	}

	for _, file := range files {
		if err := inspectFile(set, file, functions, methods); err != nil {
			return err
		}
	}
	return nil
}

func inspectFile(set *token.FileSet, file sourceFile, functions, methods map[string][]declaration) error {
	imports := importNames(file.ast)
	var validationErr error
	ast.Inspect(file.ast, func(node ast.Node) bool {
		if validationErr != nil {
			return false
		}
		switch value := node.(type) {
		case *ast.SelectorExpr:
			if value.Sel.Name == "ErrPending" || value.Sel.Name == "ErrSkip" {
				validationErr = positionError(set, file.path, value.Pos(), "pending/skip sentinel %s is forbidden", value.Sel.Name)
				return false
			}
		case *ast.CallExpr:
			selector, ok := value.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if selector.Sel.Name == "Skip" || selector.Sel.Name == "Skipf" || selector.Sel.Name == "SkipNow" {
				validationErr = positionError(set, file.path, value.Pos(), "test skip call %s is forbidden", selector.Sel.Name)
				return false
			}
			if !isStepRegistration(selector.Sel.Name) || len(value.Args) < 2 {
				return true
			}

			handler := value.Args[1]
			body, resolved := resolveHandler(handler, functions, methods, imports)
			if !resolved {
				validationErr = positionError(set, file.path, handler.Pos(), "registered step handler cannot be resolved within its package")
				return false
			}
			if isTrivialNilHandler(body) {
				validationErr = positionError(set, file.path, handler.Pos(), "registered step handler is empty or only returns nil")
				return false
			}
		}
		return true
	})
	return validationErr
}

func resolveHandler(expression ast.Expr, functions, methods map[string][]declaration, imports map[string]struct{}) (*ast.BlockStmt, bool) {
	switch handler := expression.(type) {
	case *ast.FuncLit:
		return handler.Body, true
	case *ast.Ident:
		items := functions[handler.Name]
		if len(items) == 1 {
			return items[0].body, true
		}
	case *ast.SelectorExpr:
		if receiver, ok := handler.X.(*ast.Ident); ok {
			if _, imported := imports[receiver.Name]; imported {
				return nil, false
			}
		}
		items := methods[handler.Sel.Name]
		if len(items) == 1 {
			return items[0].body, true
		}
	}
	return nil, false
}

func importNames(file *ast.File) map[string]struct{} {
	names := make(map[string]struct{})
	for _, spec := range file.Imports {
		if spec.Name != nil {
			if spec.Name.Name != "_" && spec.Name.Name != "." {
				names[spec.Name.Name] = struct{}{}
			}
			continue
		}
		path, err := strconv.Unquote(spec.Path.Value)
		if err == nil {
			names[filepath.Base(path)] = struct{}{}
		}
	}
	return names
}

func isStepRegistration(name string) bool {
	switch name {
	case "Step", "Given", "When", "Then":
		return true
	default:
		return false
	}
}

func isTrivialNilHandler(body *ast.BlockStmt) bool {
	if len(body.List) == 0 {
		return true
	}
	if len(body.List) != 1 {
		return false
	}
	statement, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(statement.Results) != 1 {
		return false
	}
	identifier, ok := statement.Results[0].(*ast.Ident)
	return ok && identifier.Name == "nil"
}

func positionError(set *token.FileSet, path string, position token.Pos, format string, args ...any) error {
	location := set.Position(position)
	message := fmt.Sprintf(format, args...)
	if location.Line == 0 {
		return fmt.Errorf("%s: %s", path, message)
	}
	return fmt.Errorf("%s:%s:%s: %s", path, strconv.Itoa(location.Line), strconv.Itoa(location.Column), strings.TrimSpace(message))
}

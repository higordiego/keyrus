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
	body         *ast.BlockStmt
	typeInfo     *ast.FuncType
	receiver     *ast.FieldList
	receiverType string
	start        token.Pos
	end          token.Pos
}

type declarations struct {
	functions map[string][]declaration
	methods   map[string][]declaration
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
	index := declarations{
		functions: make(map[string][]declaration),
		methods:   make(map[string][]declaration),
	}
	for _, file := range files {
		for _, node := range file.ast.Decls {
			function, ok := node.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			item := declaration{
				body:         function.Body,
				typeInfo:     function.Type,
				receiver:     function.Recv,
				receiverType: receiverTypeName(function.Recv),
				start:        function.Pos(),
				end:          function.End(),
			}
			if function.Recv == nil {
				index.functions[function.Name.Name] = append(index.functions[function.Name.Name], item)
			} else {
				index.methods[function.Name.Name] = append(index.methods[function.Name.Name], item)
			}
		}
	}

	for _, file := range files {
		if err := inspectFile(set, file, index); err != nil {
			return err
		}
	}
	return nil
}

func inspectFile(set *token.FileSet, file sourceFile, index declarations) error {
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
			definition, resolved := resolveHandler(handler, index)
			if !resolved {
				validationErr = positionError(set, file.path, handler.Pos(), "registered step handler cannot be proven to reference a unique local body; use a direct local function, a unique local method, or an inline handler")
				return false
			}
			if isTrivialHandler(definition, index) {
				validationErr = positionError(set, file.path, handler.Pos(), "registered step handler is empty, only returns nil, or has no observable effect")
				return false
			}
		}
		return true
	})
	return validationErr
}

func resolveHandler(expression ast.Expr, index declarations) (declaration, bool) {
	switch handler := expression.(type) {
	case *ast.FuncLit:
		return declaration{body: handler.Body, typeInfo: handler.Type, start: handler.Pos(), end: handler.End()}, true
	case *ast.Ident:
		if handler.Obj != nil {
			function, ok := handler.Obj.Decl.(*ast.FuncDecl)
			if handler.Obj.Kind != ast.Fun || !ok || function.Body == nil {

				return declaration{}, false
			}
			return declaration{
				body:         function.Body,
				typeInfo:     function.Type,
				receiver:     function.Recv,
				receiverType: receiverTypeName(function.Recv),
				start:        function.Pos(),
				end:          function.End(),
			}, true
		}
		items := index.functions[handler.Name]
		if len(items) == 1 {
			return items[0], true
		}
	case *ast.SelectorExpr:
		receiver, ok := handler.X.(*ast.Ident)
		if !ok || receiver.Obj == nil || receiver.Obj.Kind != ast.Var {

			return declaration{}, false
		}
		return resolveLocalMethod(receiver, handler.Sel.Name, index)
	}
	return declaration{}, false
}

func isStepRegistration(name string) bool {
	switch name {
	case "Step", "Given", "When", "Then":
		return true
	default:
		return false
	}
}

func isTrivialHandler(definition declaration, index declarations) bool {
	return !blockHasObservableEffect(definition, definition.body, index, make(map[*ast.BlockStmt]bool))
}

func blockHasObservableEffect(definition declaration, block *ast.BlockStmt, index declarations, visiting map[*ast.BlockStmt]bool) bool {
	if block == nil || visiting[block] {
		return false
	}
	visiting[block] = true
	defer delete(visiting, block)
	for _, statement := range block.List {
		if statementHasObservableEffect(definition, statement, index, visiting) {
			return true
		}
	}
	return false
}

func statementHasObservableEffect(definition declaration, statement ast.Stmt, index declarations, visiting map[*ast.BlockStmt]bool) bool {
	switch value := statement.(type) {
	case *ast.AssignStmt:
		for _, target := range value.Lhs {
			if targetIsObservable(definition, target) {
				return true
			}
		}
		return expressionsHaveObservableEffect(definition, value.Rhs, index, visiting)
	case *ast.IncDecStmt:
		return targetIsObservable(definition, value.X)
	case *ast.ExprStmt:
		return expressionHasObservableEffect(definition, value.X, index, visiting)
	case *ast.ReturnStmt:
		return expressionsHaveObservableEffect(definition, value.Results, index, visiting)
	case *ast.DeclStmt:
		declaration, ok := value.Decl.(*ast.GenDecl)
		if !ok {
			return false
		}
		for _, spec := range declaration.Specs {
			item, ok := spec.(*ast.ValueSpec)
			if ok && expressionsHaveObservableEffect(definition, item.Values, index, visiting) {
				return true
			}
		}
	case *ast.IfStmt:
		if value.Init != nil && statementHasObservableEffect(definition, value.Init, index, visiting) {
			return true
		}
		if expressionHasObservableEffect(definition, value.Cond, index, visiting) {
			return true
		}
		if expressionReferencesInput(definition, value.Cond) && (blockCanReject(value.Body) || statementCanReject(value.Else)) {
			return true
		}
		if blockHasObservableEffect(definition, value.Body, index, visiting) {
			return true
		}
		return value.Else != nil && statementHasObservableEffect(definition, value.Else, index, visiting)
	case *ast.ForStmt:
		if value.Init != nil && statementHasObservableEffect(definition, value.Init, index, visiting) {
			return true
		}
		if value.Cond != nil && expressionHasObservableEffect(definition, value.Cond, index, visiting) {
			return true
		}
		if value.Post != nil && statementHasObservableEffect(definition, value.Post, index, visiting) {
			return true
		}
		return blockHasObservableEffect(definition, value.Body, index, visiting)
	case *ast.RangeStmt:
		if expressionHasObservableEffect(definition, value.X, index, visiting) {
			return true
		}
		return blockHasObservableEffect(definition, value.Body, index, visiting)
	case *ast.SwitchStmt:
		if value.Init != nil && statementHasObservableEffect(definition, value.Init, index, visiting) {
			return true
		}
		if value.Tag != nil && expressionHasObservableEffect(definition, value.Tag, index, visiting) {
			return true
		}
		return blockHasObservableEffect(definition, value.Body, index, visiting)
	case *ast.TypeSwitchStmt:
		if value.Init != nil && statementHasObservableEffect(definition, value.Init, index, visiting) {
			return true
		}
		if value.Assign != nil && statementHasObservableEffect(definition, value.Assign, index, visiting) {
			return true
		}
		return blockHasObservableEffect(definition, value.Body, index, visiting)
	case *ast.SelectStmt:
		return blockHasObservableEffect(definition, value.Body, index, visiting)
	case *ast.CaseClause:
		if expressionsHaveObservableEffect(definition, value.List, index, visiting) {
			return true
		}
		for _, child := range value.Body {
			if statementHasObservableEffect(definition, child, index, visiting) {
				return true
			}
		}
	case *ast.CommClause:
		if value.Comm != nil && statementHasObservableEffect(definition, value.Comm, index, visiting) {
			return true
		}
		for _, child := range value.Body {
			if statementHasObservableEffect(definition, child, index, visiting) {
				return true
			}
		}
	case *ast.LabeledStmt:
		return statementHasObservableEffect(definition, value.Stmt, index, visiting)
	case *ast.BlockStmt:
		return blockHasObservableEffect(definition, value, index, visiting)
	case *ast.GoStmt:
		return callHasObservableEffect(definition, value.Call, index, visiting)
	case *ast.DeferStmt:
		return callHasObservableEffect(definition, value.Call, index, visiting)
	case *ast.SendStmt:
		return true
	}
	return false
}

func expressionsHaveObservableEffect(definition declaration, expressions []ast.Expr, index declarations, visiting map[*ast.BlockStmt]bool) bool {
	for _, expression := range expressions {
		if expressionHasObservableEffect(definition, expression, index, visiting) {
			return true
		}
	}
	return false
}

func expressionHasObservableEffect(definition declaration, expression ast.Expr, index declarations, visiting map[*ast.BlockStmt]bool) bool {
	switch value := expression.(type) {
	case *ast.CallExpr:
		return callHasObservableEffect(definition, value, index, visiting)
	case *ast.BinaryExpr:
		return expressionHasObservableEffect(definition, value.X, index, visiting) || expressionHasObservableEffect(definition, value.Y, index, visiting)
	case *ast.UnaryExpr:
		return expressionHasObservableEffect(definition, value.X, index, visiting)
	case *ast.ParenExpr:
		return expressionHasObservableEffect(definition, value.X, index, visiting)
	case *ast.IndexExpr:
		return expressionHasObservableEffect(definition, value.X, index, visiting) || expressionHasObservableEffect(definition, value.Index, index, visiting)
	case *ast.IndexListExpr:
		return expressionHasObservableEffect(definition, value.X, index, visiting) || expressionsHaveObservableEffect(definition, value.Indices, index, visiting)
	case *ast.SliceExpr:
		return expressionHasObservableEffect(definition, value.X, index, visiting) ||
			expressionHasObservableEffect(definition, value.Low, index, visiting) ||
			expressionHasObservableEffect(definition, value.High, index, visiting) ||
			expressionHasObservableEffect(definition, value.Max, index, visiting)
	case *ast.CompositeLit:
		return expressionsHaveObservableEffect(definition, value.Elts, index, visiting)
	case *ast.KeyValueExpr:
		return expressionHasObservableEffect(definition, value.Key, index, visiting) || expressionHasObservableEffect(definition, value.Value, index, visiting)
	case *ast.TypeAssertExpr:
		return expressionHasObservableEffect(definition, value.X, index, visiting)
	case *ast.SelectorExpr:
		return value.Sel.Name == "ErrPending" || value.Sel.Name == "ErrSkip"
	}
	return false
}

func callHasObservableEffect(definition declaration, call *ast.CallExpr, index declarations, visiting map[*ast.BlockStmt]bool) bool {
	if expressionsHaveObservableEffect(definition, call.Args, index, visiting) {
		return true
	}
	if literal, ok := call.Fun.(*ast.FuncLit); ok {
		called := declaration{body: literal.Body, typeInfo: literal.Type, start: literal.Pos(), end: literal.End()}
		return blockHasObservableEffect(called, literal.Body, index, visiting)
	}
	if called, resolved := resolveLocalCall(call.Fun, index); resolved {
		return blockHasObservableEffect(called, called.body, index, visiting)
	}
	if isKnownPureCall(call) {
		return false
	}
	return true
}

func resolveLocalCall(expression ast.Expr, index declarations) (declaration, bool) {
	switch function := expression.(type) {
	case *ast.Ident:
		if function.Obj != nil {
			declarationNode, ok := function.Obj.Decl.(*ast.FuncDecl)
			if function.Obj.Kind != ast.Fun || !ok || declarationNode.Body == nil {
				return declaration{}, false
			}
			return declaration{
				body:         declarationNode.Body,
				typeInfo:     declarationNode.Type,
				receiver:     declarationNode.Recv,
				receiverType: receiverTypeName(declarationNode.Recv),
				start:        declarationNode.Pos(),
				end:          declarationNode.End(),
			}, true
		}
		items := index.functions[function.Name]
		if len(items) == 1 {
			return items[0], true
		}
	case *ast.SelectorExpr:
		receiver, ok := function.X.(*ast.Ident)
		if !ok || receiver.Obj == nil || receiver.Obj.Kind != ast.Var {
			return declaration{}, false
		}
		return resolveLocalMethod(receiver, function.Sel.Name, index)
	}
	return declaration{}, false
}

func resolveLocalMethod(receiver *ast.Ident, methodName string, index declarations) (declaration, bool) {
	typeName, resolved := localVariableTypeName(receiver)
	if !resolved {
		return declaration{}, false
	}
	var matches []declaration
	for _, method := range index.methods[methodName] {
		if method.receiverType == typeName {
			matches = append(matches, method)
		}
	}
	if len(matches) != 1 {
		return declaration{}, false
	}
	return matches[0], true
}

func receiverTypeName(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) != 1 {
		return ""
	}
	return localTypeName(fields.List[0].Type)
}

func localVariableTypeName(identifier *ast.Ident) (string, bool) {
	if identifier == nil || identifier.Obj == nil || identifier.Obj.Kind != ast.Var {
		return "", false
	}
	switch declarationNode := identifier.Obj.Decl.(type) {
	case *ast.AssignStmt:
		for position, target := range declarationNode.Lhs {
			declared, ok := target.(*ast.Ident)
			if !ok || declared.Obj != identifier.Obj || position >= len(declarationNode.Rhs) {
				continue
			}
			name := expressionTypeName(declarationNode.Rhs[position])
			return name, name != ""
		}
	case *ast.ValueSpec:
		for position, name := range declarationNode.Names {
			if name.Obj != identifier.Obj {
				continue
			}
			if explicit := localTypeName(declarationNode.Type); explicit != "" {
				return explicit, true
			}
			if position < len(declarationNode.Values) {
				inferred := expressionTypeName(declarationNode.Values[position])
				return inferred, inferred != ""
			}
		}
	case *ast.Field:
		name := localTypeName(declarationNode.Type)
		return name, name != ""
	}
	return "", false
}

func expressionTypeName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.CompositeLit:
		return localTypeName(value.Type)
	case *ast.UnaryExpr:
		if value.Op == token.AND {
			return expressionTypeName(value.X)
		}
	case *ast.ParenExpr:
		return expressionTypeName(value.X)
	}
	return ""
}

func localTypeName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return localTypeName(value.X)
	case *ast.IndexExpr:
		return localTypeName(value.X)
	case *ast.IndexListExpr:
		return localTypeName(value.X)
	case *ast.ParenExpr:
		return localTypeName(value.X)
	}
	return ""
}

func isKnownPureCall(call *ast.CallExpr) bool {
	if identifier, ok := call.Fun.(*ast.Ident); ok {
		switch identifier.Name {
		case "bool", "byte", "cap", "complex", "error", "imag", "int", "int8", "int16", "int32", "int64", "len", "real", "rune", "string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
			return true
		}
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || (packageName.Obj != nil && packageName.Obj.Kind != ast.Pkg) {
		return false
	}
	switch packageName.Name + "." + selector.Sel.Name {
	case "context.Background", "context.TODO", "errors.New", "fmt.Errorf", "fmt.Sprintf", "fmt.Sprint", "fmt.Sprintln":
		return true
	default:
		return false
	}
}

func targetIsObservable(definition declaration, expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		if value.Name == "_" || isFunctionInput(definition, value) {
			return false
		}
		return !objectIsWithin(value.Obj, definition.start, definition.end)
	case *ast.StarExpr:
		return true
	case *ast.SelectorExpr:
		return expressionReferencesInput(definition, value.X)
	case *ast.IndexExpr:
		return expressionReferencesInput(definition, value.X)
	case *ast.ParenExpr:
		return targetIsObservable(definition, value.X)
	}
	return false
}

func expressionReferencesInput(definition declaration, expression ast.Expr) bool {
	switch value := expression.(type) {
	case nil:
		return false
	case *ast.Ident:
		if isPredeclaredIdentifier(value.Name) || value.Name == "_" {
			return false
		}
		if isFunctionInput(definition, value) {
			return true
		}
		if value.Obj == nil {
			return true
		}
		if value.Obj.Kind != ast.Var {
			return false
		}
		return !objectIsWithin(value.Obj, definition.start, definition.end)
	case *ast.SelectorExpr:

		return expressionReferencesInput(definition, value.X)
	case *ast.CallExpr:
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok && expressionReferencesInput(definition, selector.X) {
			return true
		}
		for _, argument := range value.Args {
			if expressionReferencesInput(definition, argument) {
				return true
			}
		}
	case *ast.BinaryExpr:
		return expressionReferencesInput(definition, value.X) || expressionReferencesInput(definition, value.Y)
	case *ast.UnaryExpr:
		return expressionReferencesInput(definition, value.X)
	case *ast.ParenExpr:
		return expressionReferencesInput(definition, value.X)
	case *ast.IndexExpr:
		return expressionReferencesInput(definition, value.X) || expressionReferencesInput(definition, value.Index)
	case *ast.IndexListExpr:
		if expressionReferencesInput(definition, value.X) {
			return true
		}
		for _, item := range value.Indices {
			if expressionReferencesInput(definition, item) {
				return true
			}
		}
	case *ast.SliceExpr:
		return expressionReferencesInput(definition, value.X) ||
			expressionReferencesInput(definition, value.Low) ||
			expressionReferencesInput(definition, value.High) ||
			expressionReferencesInput(definition, value.Max)
	case *ast.TypeAssertExpr:
		return expressionReferencesInput(definition, value.X)
	case *ast.KeyValueExpr:
		return expressionReferencesInput(definition, value.Key) || expressionReferencesInput(definition, value.Value)
	case *ast.CompositeLit:
		for _, item := range value.Elts {
			if expressionReferencesInput(definition, item) {
				return true
			}
		}
	}
	return false
}

func isPredeclaredIdentifier(name string) bool {
	switch name {
	case "any", "bool", "byte", "cap", "clear", "close", "comparable", "complex", "complex64", "complex128", "copy", "delete", "error", "false", "float32", "float64", "imag", "int", "int8", "int16", "int32", "int64", "iota", "len", "make", "max", "min", "new", "nil", "panic", "print", "println", "real", "recover", "rune", "string", "true", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
		return true
	default:
		return false
	}
}

func isFunctionInput(definition declaration, identifier *ast.Ident) bool {
	var parameters *ast.FieldList
	if definition.typeInfo != nil {
		parameters = definition.typeInfo.Params
	}
	for _, fields := range []*ast.FieldList{parameters, definition.receiver} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			for _, name := range field.Names {
				if name.Obj != nil && name.Obj == identifier.Obj {
					return true
				}
			}
		}
	}
	return false
}

func objectIsWithin(object *ast.Object, start, end token.Pos) bool {
	if object == nil || object.Decl == nil {
		return false
	}
	node, ok := object.Decl.(ast.Node)
	return ok && node.Pos() >= start && node.End() <= end
}

func blockCanReject(block *ast.BlockStmt) bool {
	if block == nil {
		return false
	}
	for _, statement := range block.List {
		if statementCanReject(statement) {
			return true
		}
	}
	return false
}

func statementCanReject(statement ast.Stmt) bool {
	switch value := statement.(type) {
	case *ast.ReturnStmt:
		for _, result := range value.Results {
			identifier, ok := result.(*ast.Ident)
			if !ok || identifier.Name != "nil" {
				return true
			}
		}
	case *ast.BlockStmt:
		return blockCanReject(value)
	case *ast.IfStmt:
		return blockCanReject(value.Body) || statementCanReject(value.Else)
	}
	return false
}

func positionError(set *token.FileSet, path string, position token.Pos, format string, args ...any) error {
	location := set.Position(position)
	message := fmt.Sprintf(format, args...)
	if location.Line == 0 {
		return fmt.Errorf("%s: %s", path, message)
	}
	return fmt.Errorf("%s:%s:%s: %s", path, strconv.Itoa(location.Line), strconv.Itoa(location.Column), strings.TrimSpace(message))
}

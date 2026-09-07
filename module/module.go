// Package module implements composition of named expression modules.
//
// Instead of maintaining large rules as a single copy-pasted expression
// string, common fragments can be registered as named modules and
// referenced by name (as identifiers) from the main expression and from
// other modules. Modules are parsed independently, checked for cyclic
// dependencies, and inlined into a single expression tree at compile
// time. Errors originating from a module body are attributed to the
// module they come from.
package module

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/builtin"
	"github.com/expr-lang/expr/conf"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/parser"
)

// mainName is used internally for the entry (main) expression.
const mainName = ""

// reserved words that cannot be used as module names, as they can never
// be referenced as plain identifiers.
var reserved = map[string]bool{
	"not": true, "in": true, "or": true, "and": true, "matches": true,
	"contains": true, "startsWith": true, "endsWith": true,
	"let": true, "if": true, "else": true,
	"true": true, "false": true, "nil": true,
}

type composer struct {
	config  *conf.Config
	modules map[string]string
	parsed  map[string]*parser.Tree
	deps    map[string]map[string]bool
}

// Compose parses the entry expression together with the named modules
// registered in config.Modules, resolves module references, detects
// dependency cycles, and inlines every referenced module into a single
// expression tree.
//
// A module is referenced by its name used as an identifier. The
// returned tree uses a composite source that contains the entry
// expression followed by every module body; the tree's Fragments allow
// attributing errors to the module they originate from.
func Compose(entry string, config *conf.Config) (*parser.Tree, error) {
	c := &composer{
		config:  config,
		modules: config.Modules,
		parsed:  make(map[string]*parser.Tree),
		deps:    make(map[string]map[string]bool),
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := c.parse(entry); err != nil {
		return nil, err
	}
	c.buildDeps()
	if err := c.checkCycles(); err != nil {
		return nil, err
	}
	return c.merge(entry)
}

func (c *composer) validate() error {
	for _, name := range c.sortedModuleNames() {
		if !isValidIdentifier(name) {
			return fmt.Errorf("invalid module name %q: must be a valid identifier", name)
		}
		if reserved[name] {
			return fmt.Errorf("invalid module name %q: reserved keyword", name)
		}
		if _, ok := builtin.Index[name]; ok {
			return fmt.Errorf("invalid module name %q: conflicts with builtin function", name)
		}
	}
	return nil
}

func (c *composer) parse(entry string) error {
	tree, err := parser.ParseWithConfig(entry, c.config)
	if err != nil {
		return err
	}
	c.parsed[mainName] = tree

	for _, name := range c.sortedModuleNames() {
		// Modules are parsed as fragments so they may use the predicate
		// pointer syntax and be inlined as predicates later.
		tree, err := parser.ParseFragment(c.modules[name], c.config)
		if err != nil {
			return attributeParseError(name, err)
		}
		c.parsed[name] = tree
	}
	return nil
}

// attributeParseError prefixes parse errors (already bound to the
// module's own source) with the module name.
func attributeParseError(name string, err error) error {
	var fileError *file.Error
	if errors.As(err, &fileError) {
		fileError.Message = fmt.Sprintf("in module %q: %s", name, fileError.Message)
		return fileError
	}
	return err
}

func (c *composer) buildDeps() {
	for name, tree := range c.parsed {
		deps := make(map[string]bool)
		c.collectDeps(tree.Node, newScope(), deps)
		c.deps[name] = deps
	}
}

// collectDeps walks a tree and records identifiers that reference a
// module. Identifiers bound by a `let` declaration are tracked per
// scope, as they shadow module references.
func (c *composer) collectDeps(node ast.Node, locals *scope, deps map[string]bool) {
	if node == nil {
		return
	}
	if id, ok := node.(*ast.IdentifierNode); ok {
		if _, isModule := c.modules[id.Value]; isModule && !locals.has(id.Value) {
			deps[id.Value] = true
		}
		return
	}
	if vd, ok := node.(*ast.VariableDeclaratorNode); ok {
		c.collectDeps(vd.Value, locals, deps)
		locals.add(vd.Name)
		c.collectDeps(vd.Expr, locals, deps)
		locals.remove(vd.Name)
		return
	}
	forEachChild(node, func(child *ast.Node) {
		c.collectDeps(*child, locals, deps)
	})
}

// checkCycles runs a DFS over the module dependency graph and returns
// an error describing the first cycle found, including the modules
// involved (e.g. "a -> b -> c -> a").
func (c *composer) checkCycles() error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int)
	var stack []string
	var cycle []string

	var visit func(name string) bool
	visit = func(name string) bool {
		color[name] = gray
		stack = append(stack, name)
		for _, dep := range c.sortedDeps(name) {
			switch color[dep] {
			case gray:
				i := indexOf(stack, dep)
				cycle = append(append([]string{}, stack[i:]...), dep)
				return true
			case white:
				if visit(dep) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[name] = black
		return false
	}

	for _, name := range c.sortedModuleNames() {
		if color[name] == white && visit(name) {
			break
		}
	}
	if cycle != nil {
		return fmt.Errorf("cyclic module dependency: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

// layoutOrder returns modules reachable from the entry expression in
// dependency order (dependencies before dependents).
func (c *composer) layoutOrder() []string {
	visited := make(map[string]bool)
	var order []string
	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		for _, dep := range c.sortedDeps(name) {
			visit(dep)
		}
		if name != mainName {
			order = append(order, name)
		}
	}
	visit(mainName)
	return order
}

func (c *composer) merge(entry string) (*parser.Tree, error) {
	order := c.layoutOrder()
	source, fragments, starts := c.buildSource(entry, order)

	root := c.parsed[mainName].Node
	c.expand(&root, newScope(), starts)

	return &parser.Tree{
		Node:      root,
		Source:    source,
		Fragments: fragments,
	}, nil
}

// buildSource builds a composite source that contains the entry
// expression first (so its locations stay unchanged), followed by each
// module body on its own line range preceded by a banner. It also
// returns the fragment metadata and the rune offset at which each
// fragment body starts.
func (c *composer) buildSource(entry string, order []string) (file.Source, []file.Fragment, map[string]int) {
	var b strings.Builder
	fragments := make([]file.Fragment, 0, len(order)+1)
	starts := make(map[string]int, len(order)+1)

	off := 0 // rune offset
	line := 1

	add := func(name, body string) {
		from := off
		startLine := line
		b.WriteString(body)
		b.WriteByte('\n')
		n := utf8.RuneCountInString(body)
		off += n + 1
		line += strings.Count(body, "\n") + 1
		fragments = append(fragments, file.Fragment{
			Name:     name,
			Location: file.Location{From: from, To: from + n},
			Line:     startLine,
		})
		starts[name] = from
	}

	// The main expression comes first so that its line/column numbers
	// are identical to those in the original input.
	add(mainName, entry)
	for _, name := range order {
		banner := fmt.Sprintf("// ===== module %q =====\n", name)
		b.WriteString(banner)
		off += utf8.RuneCountInString(banner)
		line++ // banner occupies one line
		add(name, c.modules[name])
	}

	return file.NewSource(b.String()), fragments, starts
}

// expand walks the tree and replaces every identifier referencing a
// module with an inlined copy of the module body. Identifiers bound by
// `let` in the current scope shadow module references.
func (c *composer) expand(node *ast.Node, locals *scope, starts map[string]int) {
	if *node == nil {
		return
	}
	if id, ok := (*node).(*ast.IdentifierNode); ok {
		if _, isModule := c.modules[id.Value]; isModule && !locals.has(id.Value) {
			*node = c.inline(id.Value, starts)
			return
		}
	}
	if vd, ok := (*node).(*ast.VariableDeclaratorNode); ok {
		c.expand(&vd.Value, locals, starts)
		locals.add(vd.Name)
		c.expand(&vd.Expr, locals, starts)
		locals.remove(vd.Name)
		return
	}
	forEachChild(*node, func(child *ast.Node) {
		c.expand(child, locals, starts)
	})
}

// inline returns a deep copy of a module's tree with all locations
// shifted to the module's position in the composite source, and with
// all module references inside it expanded as well.
func (c *composer) inline(name string, starts map[string]int) ast.Node {
	body := cloneShift(c.parsed[name].Node, starts[name])
	c.expand(&body, newScope(), starts)
	return body
}

func (c *composer) sortedModuleNames() []string {
	names := make([]string, 0, len(c.modules))
	for name := range c.modules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *composer) sortedDeps(name string) []string {
	deps := make([]string, 0, len(c.deps[name]))
	for dep := range c.deps[name] {
		deps = append(deps, dep)
	}
	sort.Strings(deps)
	return deps
}

// scope tracks identifiers bound by `let` declarations. A counter is
// used so nested scopes can shadow the same name safely.
type scope map[string]int

func newScope() *scope {
	s := make(scope)
	return &s
}

func (s scope) add(name string) {
	s[name]++
}

func (s scope) remove(name string) {
	if s[name] > 1 {
		s[name]--
	} else {
		delete(s, name)
	}
}

func (s scope) has(name string) bool {
	return s[name] > 0
}

func isValidIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

// forEachChild calls fn for every direct child node of node.
func forEachChild(node ast.Node, fn func(child *ast.Node)) {
	switch n := node.(type) {
	case *ast.UnaryNode:
		fn(&n.Node)
	case *ast.BinaryNode:
		fn(&n.Left)
		fn(&n.Right)
	case *ast.ChainNode:
		fn(&n.Node)
	case *ast.MemberNode:
		fn(&n.Node)
		fn(&n.Property)
	case *ast.SliceNode:
		fn(&n.Node)
		if n.From != nil {
			fn(&n.From)
		}
		if n.To != nil {
			fn(&n.To)
		}
	case *ast.CallNode:
		fn(&n.Callee)
		for i := range n.Arguments {
			fn(&n.Arguments[i])
		}
	case *ast.BuiltinNode:
		for i := range n.Arguments {
			fn(&n.Arguments[i])
		}
		if n.Map != nil {
			fn(&n.Map)
		}
	case *ast.PredicateNode:
		fn(&n.Node)
	case *ast.VariableDeclaratorNode:
		// Expr is handled by callers together with scope management.
		fn(&n.Value)
	case *ast.SequenceNode:
		for i := range n.Nodes {
			fn(&n.Nodes[i])
		}
	case *ast.ConditionalNode:
		fn(&n.Cond)
		fn(&n.Exp1)
		if n.Exp2 != nil {
			fn(&n.Exp2)
		}
	case *ast.ArrayNode:
		for i := range n.Nodes {
			fn(&n.Nodes[i])
		}
	case *ast.MapNode:
		for i := range n.Pairs {
			fn(&n.Pairs[i])
		}
	case *ast.PairNode:
		fn(&n.Key)
		fn(&n.Value)
	}
}

// cloneShift returns a deep copy of node with all location offsets
// shifted by shift (a rune offset within the composite source).
func cloneShift(node ast.Node, shift int) ast.Node {
	if node == nil {
		return nil
	}
	var clone ast.Node
	switch n := node.(type) {
	case *ast.NilNode:
		clone = &ast.NilNode{}
	case *ast.IdentifierNode:
		clone = &ast.IdentifierNode{Value: n.Value}
	case *ast.IntegerNode:
		clone = &ast.IntegerNode{Value: n.Value}
	case *ast.FloatNode:
		clone = &ast.FloatNode{Value: n.Value}
	case *ast.BoolNode:
		clone = &ast.BoolNode{Value: n.Value}
	case *ast.StringNode:
		clone = &ast.StringNode{Value: n.Value}
	case *ast.BytesNode:
		clone = &ast.BytesNode{Value: append([]byte(nil), n.Value...)}
	case *ast.ConstantNode:
		clone = &ast.ConstantNode{Value: n.Value}
	case *ast.UnaryNode:
		clone = &ast.UnaryNode{
			Operator: n.Operator,
			Node:     cloneShift(n.Node, shift),
		}
	case *ast.BinaryNode:
		clone = &ast.BinaryNode{
			Operator: n.Operator,
			Left:     cloneShift(n.Left, shift),
			Right:    cloneShift(n.Right, shift),
		}
	case *ast.ChainNode:
		clone = &ast.ChainNode{Node: cloneShift(n.Node, shift)}
	case *ast.MemberNode:
		clone = &ast.MemberNode{
			Node:     cloneShift(n.Node, shift),
			Property: cloneShift(n.Property, shift),
			Optional: n.Optional,
			Method:   n.Method,
		}
	case *ast.SliceNode:
		clone = &ast.SliceNode{
			Node: cloneShift(n.Node, shift),
			From: cloneShift(n.From, shift),
			To:   cloneShift(n.To, shift),
		}
	case *ast.CallNode:
		clone = &ast.CallNode{
			Callee:    cloneShift(n.Callee, shift),
			Arguments: cloneShiftSlice(n.Arguments, shift),
		}
	case *ast.BuiltinNode:
		c := &ast.BuiltinNode{
			Name:      n.Name,
			Arguments: cloneShiftSlice(n.Arguments, shift),
			Throws:    n.Throws,
			Map:       cloneShift(n.Map, shift),
		}
		if n.Threshold != nil {
			v := *n.Threshold
			c.Threshold = &v
		}
		clone = c
	case *ast.PredicateNode:
		clone = &ast.PredicateNode{Node: cloneShift(n.Node, shift)}
	case *ast.PointerNode:
		clone = &ast.PointerNode{Name: n.Name}
	case *ast.ConditionalNode:
		clone = &ast.ConditionalNode{
			Ternary: n.Ternary,
			Cond:    cloneShift(n.Cond, shift),
			Exp1:    cloneShift(n.Exp1, shift),
			Exp2:    cloneShift(n.Exp2, shift),
		}
	case *ast.VariableDeclaratorNode:
		clone = &ast.VariableDeclaratorNode{
			Name:  n.Name,
			Value: cloneShift(n.Value, shift),
			Expr:  cloneShift(n.Expr, shift),
		}
	case *ast.SequenceNode:
		clone = &ast.SequenceNode{Nodes: cloneShiftSlice(n.Nodes, shift)}
	case *ast.ArrayNode:
		clone = &ast.ArrayNode{Nodes: cloneShiftSlice(n.Nodes, shift)}
	case *ast.MapNode:
		clone = &ast.MapNode{Pairs: cloneShiftSlice(n.Pairs, shift)}
	case *ast.PairNode:
		clone = &ast.PairNode{
			Key:   cloneShift(n.Key, shift),
			Value: cloneShift(n.Value, shift),
		}
	default:
		panic(fmt.Sprintf("undefined node type (%T)", node))
	}
	loc := node.Location()
	clone.SetLocation(file.Location{
		From: loc.From + shift,
		To:   loc.To + shift,
	})
	return clone
}

func cloneShiftSlice(nodes []ast.Node, shift int) []ast.Node {
	if nodes == nil {
		return nil
	}
	clones := make([]ast.Node, len(nodes))
	for i, n := range nodes {
		clones[i] = cloneShift(n, shift)
	}
	return clones
}

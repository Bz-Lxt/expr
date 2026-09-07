//go:build expr_debug
// +build expr_debug

package vm_test

import (
	"strings"
	"testing"

	"github.com/expr-lang/expr/internal/testify/require"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/compiler"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
)

func TestDebugger(t *testing.T) {
	input := `[1, 2]`

	node, err := parser.Parse(input)
	require.NoError(t, err)

	program, err := compiler.Compile(node, nil)
	require.NoError(t, err)

	debug := vm.Debug()
	go func() {
		debug.Step()
		debug.Step()
		debug.Step()
		debug.Step()
	}()
	go func() {
		for range debug.Position() {
		}
	}()

	_, err = debug.Run(program, nil)
	require.NoError(t, err)
	require.Len(t, debug.Stack, 0)
	require.Nil(t, debug.Scopes)
}

// TestDebugger_OpParallel verifies that an OpParallel block is evaluated
// serially, instruction by instruction, when the debugger is used.
func TestDebugger_OpParallel(t *testing.T) {
	env := map[string]any{
		"f": func(i int) int { return i * 2 },
		"g": func(i int) int { return i + 1 },
	}

	program, err := expr.Compile(`[f(1), g(2)]`, expr.Env(env), expr.ConcurrentEvaluation())
	require.NoError(t, err)
	require.True(t, strings.Contains(program.Disassemble(), "OpParallel"))

	debug := vm.Debug()
	go func() {
		for i := 0; i < len(program.Bytecode); i++ {
			debug.Step()
		}
	}()
	go func() {
		for range debug.Position() {
		}
	}()

	out, err := debug.Run(program, env)
	require.NoError(t, err)
	require.Equal(t, []any{2, 3}, out)
}

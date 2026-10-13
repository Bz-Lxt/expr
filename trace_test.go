package expr_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/internal/testify/assert"
	"github.com/expr-lang/expr/internal/testify/require"
	"github.com/expr-lang/expr/vm"
)

var errBoom = errors.New("boom")

// traceEnv builds an environment whose functions fail deterministically.
func traceEnv() map[string]any {
	return map[string]any{
		"boom": func(...any) (any, error) { return nil, errBoom },
		// failOn3 errors only when called with the value 3, so the iteration
		// index of a failure can be asserted.
		"failOn3": func(v int) (any, error) {
			if v == 3 {
				return nil, errBoom
			}
			return v, nil
		},
		"xs": []int{1, 2, 3, 4, 5},
	}
}

func compileWithTrace(t *testing.T, code string, env map[string]any) *vm.Program {
	t.Helper()
	program, err := expr.Compile(code, expr.Env(env), expr.ErrorTrace())
	require.NoError(t, err)
	return program
}

func frameKinds(frames []vm.TraceFrame) []vm.TraceKind {
	kinds := make([]vm.TraceKind, len(frames))
	for i, f := range frames {
		kinds[i] = f.Kind
	}
	return kinds
}

func TestErrorTrace_NestedArrays(t *testing.T) {
	env := traceEnv()
	// Fails inside the second element of the outer array, which itself is an
	// array failing on its first element.
	code := `[[1], [boom()]]`
	program := compileWithTrace(t, code, env)

	_, err := expr.Run(program, env)
	require.Error(t, err)

	frames, ok := vm.Trace(err)
	require.True(t, ok)
	require.Len(t, frames, 3)

	require.Equal(t, vm.TraceArray, frames[0].Kind)
	assert.Equal(t, 1, frames[0].Index)
	assert.Equal(t, 6, frames[0].Location.From)

	require.Equal(t, vm.TraceArray, frames[1].Kind)
	assert.Equal(t, 0, frames[1].Index)

	require.Equal(t, vm.TraceCall, frames[2].Kind)
	assert.Equal(t, "boom", frames[2].Name)
}

func TestErrorTrace_NestedMaps(t *testing.T) {
	env := traceEnv()
	code := `{a: 1, b: {c: [boom()]}}`
	program := compileWithTrace(t, code, env)

	_, err := expr.Run(program, env)
	require.Error(t, err)

	frames, ok := vm.Trace(err)
	require.True(t, ok)
	require.Len(t, frames, 4)

	assert.Equal(t, vm.TraceMap, frames[0].Kind)
	assert.Equal(t, "b", frames[0].Key)
	assert.Equal(t, 7, frames[0].Location.From)

	assert.Equal(t, vm.TraceMap, frames[1].Kind)
	assert.Equal(t, "c", frames[1].Key)

	assert.Equal(t, vm.TraceArray, frames[2].Kind)
	assert.Equal(t, 0, frames[2].Index)

	assert.Equal(t, vm.TraceCall, frames[3].Kind)
	assert.Equal(t, "boom", frames[3].Name)
}

func TestErrorTrace_NestedCalls(t *testing.T) {
	env := traceEnv()
	code := `boom(boom())`
	program := compileWithTrace(t, code, env)

	_, err := expr.Run(program, env)
	require.Error(t, err)

	frames, ok := vm.Trace(err)
	require.True(t, ok)
	require.Len(t, frames, 2)
	for _, f := range frames {
		require.Equal(t, vm.TraceCall, f.Kind)
		assert.Equal(t, "boom", f.Name)
	}
	// Outer call first, inner call last.
	assert.Equal(t, 0, frames[0].Location.From)
	assert.Equal(t, 5, frames[1].Location.From)
}

func TestErrorTrace_IteratorIndex(t *testing.T) {
	env := traceEnv()
	code := `map(xs, failOn3(#))`
	program := compileWithTrace(t, code, env)

	_, err := expr.Run(program, env)
	require.Error(t, err)

	frames, ok := vm.Trace(err)
	require.True(t, ok)
	require.Len(t, frames, 3)

	assert.Equal(t, vm.TraceCall, frames[0].Kind)
	assert.Equal(t, "map", frames[0].Name)

	require.Equal(t, vm.TraceIterator, frames[1].Kind)
	assert.Equal(t, "map", frames[1].Name)
	assert.Equal(t, 2, frames[1].Index) // zero-based index of value 3

	assert.Equal(t, vm.TraceCall, frames[2].Kind)
	assert.Equal(t, "failOn3", frames[2].Name)
}

func TestErrorTrace_IteratorIndexBackward(t *testing.T) {
	env := traceEnv()
	// findLast iterates backwards; failOn3 must still report index 2.
	code := `findLast(xs, failOn3(#) == 3)`
	program := compileWithTrace(t, code, env)

	_, err := expr.Run(program, env)
	require.Error(t, err)

	frames, ok := vm.Trace(err)
	require.True(t, ok)

	var iter *vm.TraceFrame
	for i := range frames {
		if frames[i].Kind == vm.TraceIterator {
			iter = &frames[i]
			break
		}
	}
	require.NotNil(t, iter)
	assert.Equal(t, "findLast", iter.Name)
	assert.Equal(t, 2, iter.Index)
}

func TestErrorTrace_ShortCircuit(t *testing.T) {
	env := traceEnv()

	// boom() is never evaluated, so the expression succeeds and no error is
	// returned at all.
	program := compileWithTrace(t, `false && boom()`, env)
	out, err := expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, false, out)

	program = compileWithTrace(t, `true || boom()`, env)
	out, err = expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, true, out)

	// Untaken ternary branch is not part of the path.
	program = compileWithTrace(t, `true ? 1 : boom()`, env)
	out, err = expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, 1, out)

	// Taken branch keeps a precise path.
	program = compileWithTrace(t, `false ? 1 : [boom()]`, env)
	_, err = expr.Run(program, env)
	require.Error(t, err)
	frames, ok := vm.Trace(err)
	require.True(t, ok)
	require.Len(t, frames, 2)
	assert.Equal(t, vm.TraceArray, frames[0].Kind)
	assert.Equal(t, vm.TraceCall, frames[1].Kind)

	// Predicate short-circuiting must stop iteration before boom(): all
	// values are <= 10, so the second operand never runs.
	program = compileWithTrace(t, `filter(xs, # > 10 && boom() != nil)`, env)
	out, err = expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, []any{}, out)
}

func TestErrorTrace_DisableShortCircuit(t *testing.T) {
	env := traceEnv()
	env["flag"] = false
	// A runtime (non-foldable) left operand ensures both sides are evaluated
	// when short-circuit evaluation is disabled.
	program, err := expr.Compile(`flag && boom()`, expr.Env(env), expr.ErrorTrace(), expr.DisableShortCircuit())
	require.NoError(t, err)

	_, err = expr.Run(program, env)
	require.Error(t, err)
	frames, ok := vm.Trace(err)
	require.True(t, ok)
	require.Len(t, frames, 1)
	assert.Equal(t, vm.TraceCall, frames[0].Kind)
	assert.Equal(t, "boom", frames[0].Name)
}

func TestErrorTrace_Optimizer(t *testing.T) {
	env := traceEnv()

	// Fully constant arrays are folded; evaluation result stays correct and
	// no error leaks from folded code.
	program := compileWithTrace(t, `[[1], [2, 3]]`, env)
	out, err := expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, []any{[]any{1}, []any{2, 3}}, out)

	// Partially constant array keeps an accurate, source-based frame.
	program = compileWithTrace(t, `[1, 2, boom()]`, env)
	_, err = expr.Run(program, env)
	require.Error(t, err)
	frames, ok := vm.Trace(err)
	require.True(t, ok)
	require.Len(t, frames, 2)
	assert.Equal(t, vm.TraceArray, frames[0].Kind)
	assert.Equal(t, 2, frames[0].Index)
	// The frame points at "boom()" in the original source, not at a folded
	// or rewritten node.
	assert.Equal(t, 7, frames[0].Location.From)

	// An optimizer-evaluated ConstExpr error is still a compile-time error.
	calls := 0
	compileEnv := map[string]any{
		"constFn": func() (any, error) {
			calls++
			return nil, errBoom
		},
	}
	_, err = expr.Compile(`constFn()`, expr.Env(compileEnv), expr.ErrorTrace(), expr.ConstExpr("constFn"))
	require.Error(t, err)
	assert.Equal(t, 1, calls)
}

func TestErrorTrace_PreservesDiagnosticAndResult(t *testing.T) {
	env := traceEnv()
	code := `[boom()]`

	// Error text must be identical to a run without tracing.
	plain, err := expr.Compile(code, expr.Env(env))
	require.NoError(t, err)
	_, plainErr := expr.Run(plain, env)
	require.Error(t, plainErr)

	traced := compileWithTrace(t, code, env)
	_, tracedErr := expr.Run(traced, env)
	require.Error(t, tracedErr)

	assert.Equal(t, plainErr.Error(), tracedErr.Error())

	// The original *file.Error diagnostic remains reachable, with location
	// and snippet.
	var fileErr *file.Error
	require.True(t, errors.As(tracedErr, &fileErr))
	assert.Equal(t, 1, fileErr.Line)
	assert.Contains(t, fileErr.Error(), "boom")
	assert.NotEmpty(t, fileErr.Snippet)

	// The underlying error is still reachable via errors.Is/As.
	assert.True(t, errors.Is(tracedErr, errBoom))

	// Successful evaluation results are unchanged by instrumentation.
	program := compileWithTrace(t, `[1, 2]`, env)
	out, err := expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, []any{1, 2}, out)
}

func TestErrorTrace_DefaultOff(t *testing.T) {
	env := traceEnv()
	program, err := expr.Compile(`[boom()]`, expr.Env(env))
	require.NoError(t, err)

	_, e := expr.Run(program, env)
	require.Error(t, e)

	// Without the option the error is the plain *file.Error and no trace is
	// attached.
	var fileErr *file.Error
	require.True(t, errors.As(e, &fileErr))
	frames, ok := vm.Trace(e)
	assert.False(t, ok)
	assert.Nil(t, frames)
}

func TestErrorTrace_Concurrent(t *testing.T) {
	// Every iteration fails; many goroutines share the same immutable
	// program. Frames must be identical and never interleave.
	env := traceEnv()
	program := compileWithTrace(t, `map(xs, boom())`, env)

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := expr.Run(program, env)
			frames, ok := vm.Trace(err)
			if !ok || len(frames) != 3 {
				t.Errorf("unexpected frames: %v %+v", ok, frames)
				return
			}
			if frames[0].Kind != vm.TraceCall || frames[0].Name != "map" {
				t.Errorf("unexpected outer frame: %+v", frames[0])
			}
			if frames[1].Kind != vm.TraceIterator || frames[1].Index != 0 {
				t.Errorf("unexpected iterator frame: %+v", frames[1])
			}
			if frames[2].Kind != vm.TraceCall || frames[2].Name != "boom" {
				t.Errorf("unexpected inner frame: %+v", frames[2])
			}
		}()
	}
	wg.Wait()
}

func TestErrorTrace_ReuseAfterFailure(t *testing.T) {
	env := traceEnv()
	failing := compileWithTrace(t, `[boom()]`, env)
	ok := compileWithTrace(t, `1 + 2`, env)

	// A reused VM instance must not carry frames over between runs.
	v := &vm.VM{}

	_, err1 := v.Run(failing, env)
	frames1, traced := vm.Trace(err1)
	require.True(t, traced)
	require.Len(t, frames1, 2)

	out, err2 := v.Run(ok, env)
	require.NoError(t, err2)
	assert.Equal(t, 3, out)

	_, err3 := v.Run(failing, env)
	frames3, traced := vm.Trace(err3)
	require.True(t, traced)
	assert.Len(t, frames3, 2, "frames must not accumulate across failed runs")
}

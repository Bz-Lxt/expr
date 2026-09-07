package expr_test

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/internal/testify/assert"
	"github.com/expr-lang/expr/internal/testify/require"
)

// gateFuncs returns functions f and g which each block until release is
// closed and signal their start on the started channel.
func gateFuncs() (env map[string]any, started chan struct{}, release chan struct{}) {
	started = make(chan struct{}, 2)
	release = make(chan struct{})
	wait := func() int {
		started <- struct{}{}
		<-release
		return 1
	}
	env = map[string]any{
		"f": func() int { return wait() },
		"g": func() int { return wait() },
	}
	return env, started, release
}

func TestConcurrentEvaluation_RunsBranchesConcurrently(t *testing.T) {
	env, started, release := gateFuncs()

	program, err := expr.Compile("f() + g()", expr.Env(env), expr.ConcurrentEvaluation())
	require.NoError(t, err)

	done := make(chan struct{})
	var out any
	go func() {
		out, err = expr.Run(program, env)
		close(done)
	}()

	// Both branches must start before either is released.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("branches did not start concurrently")
		}
	}
	close(release)
	<-done
	require.NoError(t, err)
	assert.Equal(t, 2, out)
}

func TestConcurrentEvaluation_SerialByDefault(t *testing.T) {
	env, started, release := gateFuncs()

	// Without expr.ConcurrentEvaluation() the second branch must not start
	// while the first one is blocked.
	program, err := expr.Compile("f() + g()", expr.Env(env))
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		_, _ = expr.Run(program, env)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first branch did not start")
	}
	select {
	case <-started:
		t.Fatal("second branch started while the first was still running (serial execution expected)")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	<-done
}

func TestConcurrentEvaluation_PreservesOrderOfResults(t *testing.T) {
	var mu sync.Mutex
	var order []int
	env := map[string]any{
		"slow": func(i int) int {
			// Finish in reverse order to prove results are collected in
			// source order regardless of completion order.
			time.Sleep(time.Duration(10-i) * time.Millisecond)
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			return i
		},
	}

	program, err := expr.Compile("[slow(1), slow(2), slow(3)]", expr.Env(env), expr.ConcurrentEvaluation())
	require.NoError(t, err)

	out, err := expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, []any{1, 2, 3}, out)

	mu.Lock()
	defer mu.Unlock()
	// Branches really did run concurrently with out-of-order completion.
	assert.NotEqual(t, []int{1, 2, 3}, order)
}

func TestConcurrentEvaluation_Results(t *testing.T) {
	env := map[string]any{
		"dbl":  func(x int) int { return x * 2 },
		"inc":  func(x int) int { return x + 1 },
		"add":  func(a, b int) int { return a + b },
		"nums": []int{1, 2, 3, 4, 5},
	}

	tests := []struct {
		code string
		want any
	}{
		{"dbl(2) + inc(3)", 8},            // 4 + 4
		{"add(dbl(2), inc(2))", 7},        // 4 + 3
		{"[dbl(1), inc(2)]", []any{2, 3}}, // branch results in source order
		{"{a: dbl(3), b: inc(4)}", map[string]any{"a": 6, "b": 5}},
		{"map(1..3, {dbl(#) + inc(#)})", []any{4, 7, 10}},
		{"filter(nums, {dbl(#) > inc(3)})", []any{3, 4, 5}},
		{"let x = 5; [dbl(x), inc(x)]", []any{10, 6}},
		{"[(let y = dbl(3); y + 1), inc(0)]", []any{7, 1}}, // branch-local variable
		{"add(dbl(1), dbl(2)) + add(inc(1), inc(2))", 11},  // nested parallel blocks
		{"nums[inc(1):dbl(2)]", []int{3, 4}},               // concurrent slice bounds
		{"let k = 10; map(1..3, {add(dbl(#), inc(#)) + k})", []any{14, 17, 20}},
		// A loop builtin living inside a branch runs on the sub-VM with its
		// own iteration scope (backward jumps stay inside the branch range).
		{"[map(1..3, {dbl(#)}), inc(10)]", []any{[]any{2, 4, 6}, 11}},
		// Nested parallel block: an argument group inside a branch spawns
		// further isolated sub-VMs.
		{"[add(dbl(1), inc(1)), dbl(5)]", []any{4, 10}},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			program, err := expr.Compile(tt.code, expr.Env(env), expr.ConcurrentEvaluation())
			require.NoError(t, err)

			got, err := expr.Run(program, env)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConcurrentEvaluation_PreservesShortCircuit(t *testing.T) {
	var calls int32
	env := map[string]any{
		"f":       func() bool { return false },
		"g":       func() bool { return true },
		"v":       1,
		"nothing": nil,
		"h": func() bool {
			atomic.AddInt32(&calls, 1)
			return true
		},
		"hi": func() int {
			atomic.AddInt32(&calls, 1)
			return 1
		},
	}

	tests := []struct {
		code      string
		want      any
		wantCalls int32
	}{
		{"f() && h()", false, 0},               // && short-circuits on false
		{"g() || h()", true, 0},                // || short-circuits on true
		{"f() || g()", true, 0},                // neither side calls h
		{"v ?? hi()", 1, 0},                    // ?? skips the right side
		{"if f() { hi() } else { 42 }", 42, 0}, // if/else evaluates one branch
		{"nothing ?? hi()", 1, 1},              // ?? evaluates the right side when nil
		{"g() && h()", true, 1},                // h is called when the condition holds
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			atomic.StoreInt32(&calls, 0)
			program, err := expr.Compile(tt.code, expr.Env(env), expr.ConcurrentEvaluation())
			require.NoError(t, err)

			got, err := expr.Run(program, env)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantCalls, atomic.LoadInt32(&calls), "unexpected number of side-effecting calls")
		})
	}
}

func TestConcurrentEvaluation_DeterministicErrors(t *testing.T) {
	errA := errors.New("error A")
	errB := errors.New("error B")
	env := map[string]any{
		"errA": func() (int, error) { return 0, errA },
		"errB": func() (int, error) { return 0, errB },
		"ok":   func() int { return 1 },
		"add":  func(a, b int) int { return a + b },
	}

	// The error produced with concurrent evaluation must be identical to
	// the one produced with serial evaluation (message and location).
	serial, err := expr.Compile("errA() + errB()", expr.Env(env))
	require.NoError(t, err)
	_, serialErr := expr.Run(serial, env)
	require.Error(t, serialErr)

	concurrent, err := expr.Compile("errA() + errB()", expr.Env(env), expr.ConcurrentEvaluation())
	require.NoError(t, err)

	for i := 0; i < 200; i++ {
		_, err := expr.Run(concurrent, env)
		require.Error(t, err)
		assert.Equal(t, serialErr.Error(), err.Error(), "error must be the leftmost branch error on every run")
		assert.True(t, strings.Contains(err.Error(), "error A"), "expected error A, got %v", err)
	}

	cases := []struct {
		code    string
		wantSub string
	}{
		{"ok() + errB()", "error B"},          // only the right branch fails
		{"errA() + ok()", "error A"},          // only the left branch fails
		{"[ok(), errA(), errB()]", "error A"}, // leftmost failing array element
		{"{x: ok(), y: errA(), z: errB()}", "error A"},
		{"add(errA(), errB())", "error A"},      // function call arguments
		{"errA() >= 0 || ok() >= 0", "error A"}, // left of || is always evaluated
	}
	for _, tt := range cases {
		t.Run(tt.code, func(t *testing.T) {
			program, err := expr.Compile(tt.code, expr.Env(env), expr.ConcurrentEvaluation())
			require.NoError(t, err)
			_, err = expr.Run(program, env)
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tt.wantSub), "expected %q in error, got %v", tt.wantSub, err)
		})
	}
}

func TestConcurrentEvaluation_StressWithLoopsAndVariables(t *testing.T) {
	env := map[string]any{
		"dbl": func(x int) int { return x * 2 },
		"inc": func(x int) int { return x + 1 },
		"add": func(a, b int) int { return a + b },
	}
	code := `let k = 100; map(1..20, { add(dbl(#), inc(#)) + add(dbl(#), inc(#)) + k })`

	program, err := expr.Compile(code, expr.Env(env), expr.ConcurrentEvaluation())
	require.NoError(t, err)

	// Run the same program from many goroutines: the race detector (if
	// enabled) verifies isolation; results must always be identical.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := expr.Run(program, env)
			require.NoError(t, err)
			arr, ok := out.([]any)
			require.True(t, ok)
			require.Len(t, arr, 20)
			// element i (1-based): (2i + i+1) * 2 + 100 = 6i + 102
			for j := 0; j < 20; j++ {
				assert.Equal(t, 6*(j+1)+102, arr[j])
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentEvaluation_OpcodeEmission(t *testing.T) {
	env := map[string]any{
		"dbl": func(x int) int { return x * 2 },
		"inc": func(x int) int { return x + 1 },
	}

	withOption, err := expr.Compile("dbl(1) + inc(2)", expr.Env(env), expr.ConcurrentEvaluation())
	require.NoError(t, err)
	assert.Contains(t, withOption.Disassemble(), "OpParallel")

	withoutOption, err := expr.Compile("dbl(1) + inc(2)", expr.Env(env))
	require.NoError(t, err)
	assert.NotContains(t, withoutOption.Disassemble(), "OpParallel")

	// No function calls: serial compilation, no goroutine overhead.
	trivial, err := expr.Compile("1 + 2", expr.Env(env), expr.ConcurrentEvaluation())
	require.NoError(t, err)
	assert.NotContains(t, trivial.Disassemble(), "OpParallel")

	// Short-circuit operators are never compiled as parallel blocks.
	shortCircuit, err := expr.Compile("dbl(1) == 2 || inc(2) == 3", expr.Env(env), expr.ConcurrentEvaluation())
	require.NoError(t, err)
	assert.NotContains(t, shortCircuit.Disassemble(), "OpParallel")
}

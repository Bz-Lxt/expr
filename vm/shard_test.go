package vm_test

import (
	"fmt"
	"testing"

	"github.com/expr-lang/expr/internal/testify/require"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// shardStepSizes are the shard sizes used to run programs. Size 1 pauses
// between every single opcode — the strongest possible check that resume
// continues exactly where the previous shard stopped.
var shardStepSizes = []int{1, 2, 3, 5, 7, 1000}

// runSharded drives a cooperative, sharded run to completion: each shard
// executes at most maxSteps opcodes and then yields at a safe point. After
// the first shard it passes nil program/env to exercise the resume path.
func runSharded(t *testing.T, v *vm.VM, program *vm.Program, env any, maxSteps int) any {
	t.Helper()
	var out any
	for {
		got, state, err := v.RunShard(program, env, maxSteps)
		require.NoError(t, err)
		if state == vm.StateDone {
			out = got
			break
		}
		require.Equal(t, vm.StatePaused, state)
		require.Nil(t, got)
		require.True(t, v.Paused(), "VM must be paused between shards")
		program = nil
		env = nil
	}
	require.False(t, v.Paused(), "VM must not be paused after the final shard")
	return out
}

func TestRunShard_ResultsMatchRun(t *testing.T) {
	cases := []struct {
		name  string
		input string
		env   any
	}{
		{"arith", `1 + 2*3 - 4`, nil},
		{"map", `map(1..10, { # * 2 })`, nil},
		{"filter", `filter(1..20, { # % 3 == 0 })`, nil},
		{"let", `let x = 10; map(1..5, { # + x })`, nil},
		{"nested", `map(1..3, { filter(1..5, { # % 2 == 0 }) })`, nil},
		{"reduce", `reduce(1..10, # + #acc, 0)`, nil},
		{"sortBy", `sortBy([3, 1, 4, 1, 5, 9, 2, 6], #)`, nil},
		{"ternary", `map(1..5, { # > 2 ? # : 0 })`, nil},
		{"all_any", `all(1..5, # > 0) and any(1..5, # == 3)`, nil},
		{"join", `join(map(1..3, { string(#) }), ",")`, nil},
		{"upper", `map(["a", "b"], { upper(#) })`, nil},
		{"fields", `map(users, { .name })`, map[string]any{
			"users": []map[string]any{{"name": "a"}, {"name": "b"}},
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := []expr.Option{}
			if c.env != nil {
				opts = append(opts, expr.Env(c.env))
			}
			program, err := expr.Compile(c.input, opts...)
			require.NoError(t, err)

			want, err := expr.Run(program, c.env)
			require.NoError(t, err)

			for _, maxSteps := range shardStepSizes {
				t.Run(fmt.Sprintf("steps=%d", maxSteps), func(t *testing.T) {
					v := vm.VM{}
					got := runSharded(t, &v, program, c.env, maxSteps)
					require.Equal(t, want, got)
				})
			}
		})
	}
}

// TestRunShard_NoRepeatedSideEffects is the core guarantee: pausing at opcode
// boundaries and resuming must never re-execute a function-call opcode, so
// side-effecting functions are invoked exactly once per call site, regardless
// of how small the shards are.
func TestRunShard_NoRepeatedSideEffects(t *testing.T) {
	t.Run("registered functions", func(t *testing.T) {
		var bumpCalls, pingCalls, addCalls, mark3Calls, tag4Calls int
		opts := []expr.Option{
			expr.Function("bump", func(p ...any) (any, error) {
				bumpCalls++
				return p[0], nil
			}, new(func(int) int)),
			expr.Function("ping", func(p ...any) (any, error) {
				pingCalls++
				return 1, nil
			}, new(func() int)),
			expr.Function("add", func(p ...any) (any, error) {
				addCalls++
				return p[0].(int) + p[1].(int), nil
			}, new(func(int, int) int)),
			expr.Function("mark3", func(p ...any) (any, error) {
				mark3Calls++
				return p[0].(int), nil
			}, new(func(int, int, int) int)),
			expr.Function("tag4", func(p ...any) (any, error) {
				tag4Calls++
				return p[0].(int), nil
			}, new(func(int, int, int, int) int)),
		}
		// bump -> OpCall1, ping -> OpCall0, add -> OpCall2,
		// mark3 -> OpCall3, tag4 -> OpCallN.
		program, err := expr.Compile(
			`map(1..8, { bump(#) + ping() + add(#, 1) + mark3(#, 1, 2) + tag4(#, 1, 2, 3) })`,
			opts...)
		require.NoError(t, err)

		want, err := vm.Run(program, nil)
		require.NoError(t, err)
		require.Equal(t, 8, bumpCalls)
		require.Equal(t, 8, pingCalls)
		require.Equal(t, 8, addCalls)
		require.Equal(t, 8, mark3Calls)
		require.Equal(t, 8, tag4Calls)

		bumpCalls, pingCalls, addCalls, mark3Calls, tag4Calls = 0, 0, 0, 0, 0
		v := vm.VM{}
		got := runSharded(t, &v, program, nil, 1) // pause after every opcode
		require.Equal(t, want, got)
		require.Equal(t, 8, bumpCalls)
		require.Equal(t, 8, pingCalls)
		require.Equal(t, 8, addCalls)
		require.Equal(t, 8, mark3Calls)
		require.Equal(t, 8, tag4Calls)
	})

	t.Run("fast and reflect env functions", func(t *testing.T) {
		var tapCalls, totalCalls int
		env := map[string]any{
			// tap -> OpCallFast.
			"tap": func(p ...any) any {
				tapCalls++
				return p[0]
			},
			// total is variadic with a concrete return type, so it is neither
			// a fast func nor a typed one -> OpCall (reflect).
			"total": func(nums ...int) int {
				totalCalls++
				sum := 0
				for _, n := range nums {
					sum += n
				}
				return sum
			},
		}
		program, err := expr.Compile(`map(1..6, { tap(#) + total(#, #) })`, expr.Env(env))
		require.NoError(t, err)

		want, err := vm.Run(program, env)
		require.NoError(t, err)
		require.Equal(t, 6, tapCalls)
		require.Equal(t, 6, totalCalls)

		tapCalls, totalCalls = 0, 0
		v := vm.VM{}
		got := runSharded(t, &v, program, env, 1)
		require.Equal(t, want, got)
		require.Equal(t, 6, tapCalls)
		require.Equal(t, 6, totalCalls)
	})

	t.Run("typed env method", func(t *testing.T) {
		// A method on a struct env compiles to OpCallTyped.
		m := &methodEnv{}
		program, err := expr.Compile(`map(1..5, { Bump(#) })`, expr.Env(&methodEnv{}))
		require.NoError(t, err)

		want, err := vm.Run(program, m)
		require.NoError(t, err)
		require.Equal(t, 5, m.calls)

		m.calls = 0
		v := vm.VM{}
		got := runSharded(t, &v, program, m, 1)
		require.Equal(t, want, got)
		require.Equal(t, 5, m.calls)
	})
}

type methodEnv struct {
	calls int
}

func (m *methodEnv) Bump(x int) int {
	m.calls++
	return x
}

func TestRunShard_Lifecycle(t *testing.T) {
	program, err := expr.Compile(`map(1..100, { # * 2 })`)
	require.NoError(t, err)

	v := vm.VM{}
	require.False(t, v.Paused())

	// Cannot resume a run that was never started.
	_, _, err = v.Resume(10)
	require.Error(t, err)

	// First shard starts the run and then pauses at a safe point.
	out, state, err := v.RunShard(program, nil, 5)
	require.NoError(t, err)
	require.Equal(t, vm.StatePaused, state)
	require.Nil(t, out)
	require.True(t, v.Paused())

	// Resume drives the paused run to completion.
	for {
		out, state, err = v.Resume(7)
		require.NoError(t, err)
		if state == vm.StateDone {
			break
		}
		require.Equal(t, vm.StatePaused, state)
	}
	require.False(t, v.Paused())
	require.Len(t, out.([]any), 100)

	// Resume after completion is an error (nothing is paused).
	_, _, err = v.Resume(7)
	require.Error(t, err)

	// Run on a paused VM deliberately discards the paused state and starts a
	// fresh, one-shot execution.
	v2 := vm.VM{}
	_, state, err = v2.RunShard(program, nil, 3)
	require.NoError(t, err)
	require.Equal(t, vm.StatePaused, state)
	require.True(t, v2.Paused())
	oneShot, err := v2.Run(program, nil)
	require.NoError(t, err)
	require.False(t, v2.Paused())
	require.Len(t, oneShot.([]any), 100)
}

func TestRunShard_InvalidArgs(t *testing.T) {
	program, err := expr.Compile(`1`)
	require.NoError(t, err)

	v := vm.VM{}
	_, _, err = v.RunShard(nil, nil, 10)
	require.Error(t, err) // nil program on a fresh VM

	_, _, err = v.RunShard(program, nil, 0)
	require.Error(t, err) // maxSteps must be positive

	_, _, err = v.RunShard(program, nil, -1)
	require.Error(t, err)
}

// A runtime error leaves the VM mid-opcode, so the run is terminal and not
// resumable (resuming would re-execute the failing call and repeat its side
// effects). The VM can still start a fresh run afterwards.
func TestRunShard_ErrorIsTerminal(t *testing.T) {
	var calls int
	boom := expr.Function("boom", func(p ...any) (any, error) {
		calls++
		if p[0].(int) == 3 {
			return nil, fmt.Errorf("boom on %v", p[0])
		}
		return p[0], nil
	}, new(func(int) (int, error)))

	program, err := expr.Compile(`map(1..5, { boom(#) })`, boom)
	require.NoError(t, err)

	v := vm.VM{}
	var runErr error
	for {
		_, state, err := v.RunShard(program, nil, 1)
		if err != nil {
			runErr = err
			break
		}
		if state == vm.StateDone {
			break
		}
	}
	require.Error(t, runErr)
	require.Contains(t, runErr.Error(), "boom on 3")
	require.False(t, v.Paused(), "a failed run must not be resumable")

	_, _, err = v.Resume(10)
	require.Error(t, err)

	// A fresh run can be started on the same VM after the error. This
	// program only calls boom with 1 and 2, so it succeeds.
	callsAtFailure := calls
	okProgram, err := expr.Compile(`map(1..2, { boom(#) })`, boom)
	require.NoError(t, err)
	out := runSharded(t, &v, okProgram, nil, 1)
	require.Equal(t, []any{1, 2}, out)
	require.Equal(t, callsAtFailure+2, calls, "restarting after an error re-runs from the start")
}

// A cooperative scheduler can interleave shards of several rules: each rule
// makes progress, yields at a safe point, and later resumes without any
// function side effect being repeated.
func TestRunShard_CooperativeInterleaving(t *testing.T) {
	var countA, countB int
	progA, err := expr.Compile(`map(1..12, { tickA(#) })`,
		expr.Function("tickA", func(p ...any) (any, error) {
			countA++
			return p[0], nil
		}))
	require.NoError(t, err)
	progB, err := expr.Compile(`map(1..7, { tickB(#) })`,
		expr.Function("tickB", func(p ...any) (any, error) {
			countB++
			return p[0], nil
		}))
	require.NoError(t, err)

	// Reference results (and baseline call counts) from one-shot runs.
	wantA, err := vm.Run(progA, nil)
	require.NoError(t, err)
	wantB, err := vm.Run(progB, nil)
	require.NoError(t, err)
	countA, countB = 0, 0

	var vA, vB vm.VM
	var resA, resB any
	doneA, doneB := false, false
	for !doneA || !doneB {
		if !doneA {
			out, state, err := vA.RunShard(progA, nil, 4)
			require.NoError(t, err)
			if state == vm.StateDone {
				doneA = true
				resA = out
			}
		}
		if !doneB {
			out, state, err := vB.RunShard(progB, nil, 4)
			require.NoError(t, err)
			if state == vm.StateDone {
				doneB = true
				resB = out
			}
		}
	}

	require.Equal(t, wantA, resA)
	require.Equal(t, wantB, resB)
	require.Equal(t, 12, countA)
	require.Equal(t, 7, countB)
}

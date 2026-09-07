package expr_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/internal/testify/assert"
	"github.com/expr-lang/expr/internal/testify/require"
)

func TestModule_Basic(t *testing.T) {
	env := map[string]any{
		"role": "admin",
		"age":  20,
	}
	program, err := expr.Compile(`isAdmin and isAdult`,
		expr.Env(env),
		expr.Module("isAdmin", `role == "admin"`),
		expr.Module("isAdult", `age >= 18`),
	)
	require.NoError(t, err)

	out, err := expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, true, out)
}

func TestModule_Transitive(t *testing.T) {
	env := map[string]any{
		"role":      "admin",
		"verified":  true,
		"published": true,
	}
	// canEdit -> isAdmin -> role; entry -> canEdit
	program, err := expr.Compile(`canEdit and published`,
		expr.Env(env),
		expr.Module("isAdmin", `role == "admin"`),
		expr.Module("canEdit", `isAdmin and verified`),
	)
	require.NoError(t, err)

	out, err := expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, true, out)
}

// A module referenced more than once must be inlined as independent
// copies (so later AST mutations do not alias).
func TestModule_Diamond(t *testing.T) {
	env := map[string]any{"n": 6}
	program, err := expr.Compile(`positive and not positive or n == 6`,
		expr.Env(env),
		expr.Module("positive", `n > 0`),
	)
	require.NoError(t, err)

	out, err := expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, true, out)
}

func TestModule_UsedInPredicate(t *testing.T) {
	env := map[string]any{
		"items": []map[string]any{{"v": 1}, {"v": 2}, {"v": 3}},
	}
	program, err := expr.Compile(`filter(items, big)`,
		expr.Env(env),
		expr.Module("big", `.v > 1`),
	)
	require.NoError(t, err)

	out, err := expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, []any{map[string]any{"v": 2}, map[string]any{"v": 3}}, out)
}

func TestModule_MemberAccess(t *testing.T) {
	env := map[string]any{"x": 5}
	program, err := expr.Compile(`threshold.x`,
		expr.Env(env),
		expr.Module("threshold", `{"x": x + 1}`),
	)
	require.NoError(t, err)

	out, err := expr.Run(program, env)
	require.NoError(t, err)
	assert.Equal(t, 6, out)
}

func TestModule_LetShadowing(t *testing.T) {
	// The local `let` binding shadows the module of the same name;
	// the module body must not be inlined there.
	program, err := expr.Compile(`let isAdmin = false; isAdmin`,
		expr.Module("isAdmin", `true`),
	)
	require.NoError(t, err)

	out, err := expr.Run(program, nil)
	require.NoError(t, err)
	assert.Equal(t, false, out)
}

func TestModule_LetShadowingInsideModule(t *testing.T) {
	program, err := expr.Compile(`m`,
		expr.Module("m", `let x = 1; x == 1`),
	)
	require.NoError(t, err)

	out, err := expr.Run(program, nil)
	require.NoError(t, err)
	assert.Equal(t, true, out)
}

func TestModule_UnusedIsIgnored(t *testing.T) {
	// An unused module is not inlined and must not affect compilation.
	program, err := expr.Compile(`1 + 1`,
		expr.Module("unused", `true`),
	)
	require.NoError(t, err)

	out, err := expr.Run(program, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, out)

	// Unused module body is not part of the program source.
	assert.NotContains(t, program.Source().String(), "unused")
}

func TestModule_CycleSelf(t *testing.T) {
	_, err := expr.Compile(`a`,
		expr.Module("a", `a`),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic module dependency: a -> a")
}

func TestModule_CycleTwo(t *testing.T) {
	_, err := expr.Compile(`a`,
		expr.Module("a", `b`),
		expr.Module("b", `a`),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic module dependency: a -> b -> a")
}

func TestModule_CycleThree(t *testing.T) {
	_, err := expr.Compile(`a`,
		expr.Module("a", `b`),
		expr.Module("b", `c`),
		expr.Module("c", `a`),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic module dependency: a -> b -> c -> a")
}

func TestModule_CycleUnreachableStillReported(t *testing.T) {
	_, err := expr.Compile(`1`,
		expr.Module("a", `b`),
		expr.Module("b", `a`),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic module dependency")
}

func TestModule_InvalidNames(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"keyword", "if"},
		{"builtin", "len"},
		{"not identifier", "has-dash"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := expr.Compile(`true`,
				expr.Module(tc.expr, `true`),
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid module name")
		})
	}
}

func TestModule_ParseErrorAttributed(t *testing.T) {
	_, err := expr.Compile(`ok`,
		expr.Module("ok", `true`),
		expr.Module("broken", `1 +`),
	)
	require.Error(t, err)
	var fileError *file.Error
	require.True(t, errors.As(err, &fileError))
	assert.Contains(t, err.Error(), `in module "broken"`)
	// Snippet shows the offending module source, not the entry.
	assert.Contains(t, err.Error(), "1 +")
}

func TestModule_TypeErrorAttributed(t *testing.T) {
	_, err := expr.Compile(`ok and bad`,
		expr.Module("ok", `true`),
		expr.Module("bad", `1 + "x"`),
	)
	require.Error(t, err)
	var fileError *file.Error
	require.True(t, errors.As(err, &fileError))
	assert.Contains(t, err.Error(), `in module "bad"`)
	assert.Contains(t, err.Error(), "mismatched types")
}

func TestModule_TypeErrorLocalLine(t *testing.T) {
	// Module body: error is on the 2nd line of the module.
	body := "true\n1 + \"x\""
	_, err := expr.Compile(`bad`,
		expr.Module("bad", body),
	)
	require.Error(t, err)
	var fileError *file.Error
	require.True(t, errors.As(err, &fileError))
	assert.Contains(t, err.Error(), `in module "bad"`)
	// Reported line must be relative to the module, not the composite source.
	assert.Contains(t, err.Error(), "(2:")
}

func TestModule_ErrorInMainNotAttributed(t *testing.T) {
	// An error in the entry expression keeps its original line/col
	// and must not be prefixed with a module name.
	_, err := expr.Compile(`1 + "x"`,
		expr.Module("m", `true`),
	)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "in module")
	assert.True(t, strings.Contains(err.Error(), "(1:"))
}

func TestModule_RuntimeErrorAttributed(t *testing.T) {
	program, err := expr.Compile(`u`,
		expr.AllowUndefinedVariables(),
		expr.Module("u", `user.name`),
	)
	require.NoError(t, err)

	_, err = expr.Run(program, map[string]any{"user": nil})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `in module "u"`)
}

func TestModule_RuntimeErrorInMainNotAttributed(t *testing.T) {
	program, err := expr.Compile(`user.name`,
		expr.AllowUndefinedVariables(),
		expr.Module("m", `true`),
	)
	require.NoError(t, err)

	_, err = expr.Run(program, map[string]any{"user": nil})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "in module")
}

func TestModule_PointerOutsidePredicateRejected(t *testing.T) {
	// A fragment using predicate pointer syntax can only be inlined
	// into a predicate position; using it as a plain value is a
	// checker error attributed to the module.
	_, err := expr.Compile(`big`,
		expr.AllowUndefinedVariables(),
		expr.Module("big", `.v > 1`),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `in module "big"`)
	assert.Contains(t, err.Error(), "pointer accessor outside predicate")
}

func TestModule_CompositeSource(t *testing.T) {
	program, err := expr.Compile(`m2`,
		expr.Module("m1", `1`),
		expr.Module("m2", `m1 + 1`),
	)
	require.NoError(t, err)

	src := program.Source().String()
	assert.True(t, strings.HasPrefix(src, "m2\n"))
	assert.Contains(t, src, `// ===== module "m1" =====`)
	assert.Contains(t, src, `// ===== module "m2" =====`)
	// m1 is a dependency of m2, so it is laid out before m2.
	assert.True(t, strings.Index(src, `"m1"`) < strings.Index(src, `"m2"`))
}

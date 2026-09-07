package module

import (
	"testing"
	"unicode/utf8"

	"github.com/expr-lang/expr/conf"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/internal/testify/assert"
	"github.com/expr-lang/expr/internal/testify/require"
)

func runeSlice(src string, from, to int) string {
	r := []rune(src)
	return string(r[from:to])
}

func TestCompose_FragmentMetadata(t *testing.T) {
	config := conf.CreateNew()
	config.Modules["m"] = "1 + 2"

	tree, err := Compose("m", config)
	require.NoError(t, err)
	require.Len(t, tree.Fragments, 2)

	// The main expression comes first at offset 0, line 1.
	main := tree.Fragments[0]
	assert.Equal(t, "", main.Name)
	assert.Equal(t, 1, main.Line)
	assert.Equal(t, 0, main.Location.From)
	assert.Equal(t, runeSlice("m", 0, 1), runeSlice(tree.Source.String(), main.Location.From, main.Location.To))

	// The module body follows the banner.
	mod := tree.Fragments[1]
	assert.Equal(t, "m", mod.Name)
	assert.Equal(t, "1 + 2", runeSlice(tree.Source.String(), mod.Location.From, mod.Location.To))
}

func TestCompose_OnlyReachableModulesInlined(t *testing.T) {
	config := conf.CreateNew()
	config.Modules["used"] = "1"
	config.Modules["unused"] = "2"

	tree, err := Compose("used", config)
	require.NoError(t, err)

	src := tree.Source.String()
	assert.Contains(t, src, "used")
	assert.NotContains(t, src, "unused")
}

func TestCompose_CycleError(t *testing.T) {
	config := conf.CreateNew()
	config.Modules["a"] = "b"
	config.Modules["b"] = "a"

	_, err := Compose("a", config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic module dependency: a -> b -> a")
}

func TestCompose_RuneOffsets(t *testing.T) {
	// Multi-byte runes must not shift fragment offsets (offsets are
	// counted in runes, matching file.Location semantics).
	config := conf.CreateNew()
	config.Modules["a"] = `"λx"`                  // contains multi-byte runes
	config.Modules["b"] = "true and\na == \"λx\"" // spans two lines

	tree, err := Compose("b", config)
	require.NoError(t, err)

	var b *file.Fragment
	for i := range tree.Fragments {
		if tree.Fragments[i].Name == "b" {
			b = &tree.Fragments[i]
		}
	}
	require.NotNil(t, b)

	// Simulate a checker error at `a` on the second line of module b.
	localOffset := utf8.RuneCountInString("true and") + 1 // line 1 + newline
	e := &file.Error{
		Location: file.Location{From: b.Location.From + localOffset},
		Message:  "boom",
	}
	e.Bind(tree.Source)
	attributed := e.Attribute(tree.Fragments)
	require.True(t, attributed)
	// Line must be module-local (2), not the composite source line.
	assert.Equal(t, 2, e.Line)
	assert.Contains(t, e.Message, `in module "b"`)
	// Snippet shows the module's code line.
	assert.Contains(t, e.Snippet, `a == "λx"`)
}

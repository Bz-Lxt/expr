package file

import "strings"

type Source struct {
	raw string
}

func NewSource(contents string) Source {
	return Source{
		raw: contents,
	}
}

func (s Source) String() string {
	return s.raw
}

// Fragment describes a named source fragment inside a composite source
// (for example, a named expression module inlined into a larger program).
// It is used to attribute errors to the fragment they originate from.
type Fragment struct {
	Name     string   // fragment name (e.g. module name); empty for the main expression
	Location Location // [From, To) rune range of the fragment body within the source
	Line     int      // 1-based line of the fragment body start within the source
}

func (s Source) Snippet(line int) (string, bool) {
	if s.raw == "" {
		return "", false
	}
	var start int
	for i := 1; i < line; i++ {
		pos := strings.IndexByte(s.raw[start:], '\n')
		if pos < 0 {
			return "", false
		}
		start += pos + 1
	}
	end := start + strings.IndexByte(s.raw[start:], '\n')
	if end < start {
		end = len(s.raw)
	}
	return s.raw[start:end], true
}

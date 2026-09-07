package file

import (
	"fmt"
	"strings"
)

type Error struct {
	Location
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
	Snippet string `json:"snippet"`
	Prev    error  `json:"prev"`
}

func (e *Error) Error() string {
	return e.format()
}

var tabReplacer = strings.NewReplacer("\t", " ")

func (e *Error) Bind(source Source) *Error {
	src := source.String()

	var runeCount, lineStart int
	e.Line = 1
	e.Column = 0
	for i, r := range src {
		if runeCount == e.From {
			break
		}
		if r == '\n' {
			lineStart = i + 1
			e.Line++
			e.Column = 0
		} else {
			e.Column++
		}
		runeCount++
	}

	lineEnd := lineStart + strings.IndexByte(src[lineStart:], '\n')
	if lineEnd < lineStart {
		lineEnd = len(src)
	}
	if lineStart == lineEnd {
		return e
	}

	const prefix = "\n | "
	line := src[lineStart:lineEnd]
	snippet := new(strings.Builder)
	snippet.Grow(2*len(prefix) + len(line) + e.Column + 1)
	snippet.WriteString(prefix)
	tabReplacer.WriteString(snippet, line)
	snippet.WriteString(prefix)
	for i := 0; i < e.Column; i++ {
		snippet.WriteByte('.')
	}
	snippet.WriteByte('^')
	e.Snippet = snippet.String()
	return e
}

// Attribute attributes a bound error to a named source fragment. If the
// error location falls inside a named fragment, the line is rewritten to
// be fragment-local and the message is prefixed with the fragment name.
//
// Attribute must be called after Bind, as it relies on the resolved line
// and on the location offsets being relative to the same source the
// fragments were computed for. It returns true when the error was
// attributed to a named fragment.
func (e *Error) Attribute(fragments []Fragment) bool {
	if e == nil || len(fragments) == 0 {
		return false
	}
	for _, f := range fragments {
		if f.Name == "" {
			continue
		}
		if e.From >= f.Location.From && e.From < f.Location.To {
			e.Line = e.Line - f.Line + 1
			e.Message = fmt.Sprintf("in module %q: %s", f.Name, e.Message)
			return true
		}
	}
	return false
}

func (e *Error) Unwrap() error {
	return e.Prev
}

func (e *Error) Wrap(err error) {
	e.Prev = err
}

func (e *Error) format() string {
	if e.Snippet == "" {
		return e.Message
	}
	return fmt.Sprintf(
		"%s (%d:%d)%s",
		e.Message,
		e.Line,
		e.Column+1, // add one to the 0-based column for display
		e.Snippet,
	)
}

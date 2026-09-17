package file

import (
	"fmt"
	"sort"
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

// Errors is a collection of independent diagnostics.
type Errors []*Error

// Diagnostics deduplicates and stably sorts given errors by their position
// (From, then To), using the message as a final tie-breaker. It returns nil
// if no errors are provided.
func Diagnostics(errs ...*Error) Errors {
	if len(errs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(errs))
	out := make(Errors, 0, len(errs))
	for _, e := range errs {
		key := fmt.Sprintf("%d|%d|%s", e.From, e.To, e.Message)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Message < out[j].Message
	})
	return out
}

func (errs Errors) Error() string {
	messages := make([]string, len(errs))
	for i, e := range errs {
		messages[i] = e.Error()
	}
	return strings.Join(messages, "\n")
}

// Bind binds all diagnostics to the source.
func (errs Errors) Bind(source Source) Errors {
	for _, e := range errs {
		e.Bind(source)
	}
	return errs
}

// Unwrap allows errors.Is and errors.As to inspect each individual diagnostic.
func (errs Errors) Unwrap() []error {
	out := make([]error, len(errs))
	for i, e := range errs {
		out[i] = e
	}
	return out
}

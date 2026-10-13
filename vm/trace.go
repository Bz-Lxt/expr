package vm

import (
	"errors"

	"github.com/expr-lang/expr/file"
)

// TraceKind identifies the kind of an error trace frame.
type TraceKind int

const (
	// TraceCall is a function, method or builtin function call.
	TraceCall TraceKind = iota
	// TraceArray is an element of an array literal.
	TraceArray
	// TraceMap is a key-value pair of a map literal.
	TraceMap
	// TraceIterator is a single iteration of a collection builtin such as
	// filter, map, all, any or reduce.
	TraceIterator
)

// String returns the name of the trace frame kind.
func (k TraceKind) String() string {
	switch k {
	case TraceCall:
		return "call"
	case TraceArray:
		return "array"
	case TraceMap:
		return "map"
	case TraceIterator:
		return "iterator"
	default:
		return "unknown"
	}
}

// TraceFrame is one layer of the structured path that led to a runtime
// evaluation error. Frames are ordered from the outermost layer to the
// innermost one; the failing operation itself is reported by the wrapped
// file.Error location.
type TraceFrame struct {
	// Kind is the kind of the frame.
	Kind TraceKind `json:"kind"`
	// Name is the function or builtin name for TraceCall and TraceIterator
	// frames. It is empty for TraceArray and TraceMap frames.
	Name string `json:"name,omitempty"`
	// Index is the zero-based element index for TraceArray frames and the
	// iteration index for TraceIterator frames. It is -1 when not applicable.
	Index int `json:"index"`
	// Key is the map key for TraceMap frames. It is empty otherwise.
	Key string `json:"key,omitempty"`
	// Location is the original source location of the node that introduced
	// this layer, independent of optimization and short-circuit rewrites.
	Location file.Location `json:"location"`
}

// TraceError wraps a *file.Error with the structured outer-to-inner failure
// path captured while evaluating an instrumented program.
//
// The original diagnostic keeps its fields (Line, Column, Snippet, Message)
// and error text, and its Unwrap chain still leads to the underlying error,
// so errors.Is, errors.As and existing diagnostics keep working.
type TraceError struct {
	// Diagnostic is the original runtime error, including its location and
	// snippet.
	Diagnostic *file.Error `json:"diagnostic"`
	// Frames are ordered from the outermost to the innermost layer.
	Frames []TraceFrame `json:"frames"`
}

// Error returns the original error text of the wrapped file.Error.
func (e *TraceError) Error() string {
	return e.Diagnostic.Error()
}

// Unwrap returns the wrapped *file.Error so that the standard errors.Is and
// errors.As functions visit the diagnostic before the underlying error.
func (e *TraceError) Unwrap() error {
	return e.Diagnostic
}

// Trace unwraps err to a *TraceError and returns its frames. The boolean
// result reports whether a trace was attached to the error.
func Trace(err error) ([]TraceFrame, bool) {
	var traceErr *TraceError
	if errors.As(err, &traceErr) {
		return traceErr.Frames, true
	}
	return nil, false
}

// TraceInfo is a compile-time, immutable descriptor of a trace frame stored
// among the program constants.
type TraceInfo struct {
	Kind  TraceKind
	Index int
	Key   string
	Name  string
}

// traceMarker is the runtime state of an active trace frame. Markers live on
// the VM instance and never on the shared Program, so concurrent evaluations
// cannot interfere with each other.
type traceMarker struct {
	// ip is the instruction pointer of the OpTraceEnter/OpTraceIterEnter
	// opcode, used to resolve the frame's source location on failure.
	ip int
	// depth is the scope depth captured by TraceIterator frames; it is -1
	// for statically described frames.
	depth int
	kind  TraceKind
	index int
	key   string
	name  string
}

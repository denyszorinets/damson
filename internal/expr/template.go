package expr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	openTag  = "${{"
	closeTag = "}}"
	// An escaped opening tag: "$${{" renders as the literal "${{".
	escapedOpen = "$" + openTag
)

// Segment is a literal run or one ${{ }} expression of a template string.
type Segment struct {
	Literal string
	Expr    string
	IsExpr  bool
}

// ParseTemplate splits s into literal and expression segments. The closing
// "}}" is the first one outside a quoted string, so CEL string literals may
// contain braces. "$${{" yields a literal "${{".
func ParseTemplate(s string) ([]Segment, error) {
	var segs []Segment
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			segs = append(segs, Segment{Literal: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], escapedOpen):
			lit.WriteString(openTag)
			i += len(escapedOpen)
		case strings.HasPrefix(s[i:], openTag):
			end, err := findClose(s, i+len(openTag))
			if err != nil {
				return nil, err
			}
			src := strings.TrimSpace(s[i+len(openTag) : end])
			if src == "" {
				return nil, fmt.Errorf("%w: empty expression", ErrCompile)
			}
			flush()
			segs = append(segs, Segment{Expr: src, IsExpr: true})
			i = end + len(closeTag)
		default:
			lit.WriteByte(s[i])
			i++
		}
	}
	flush()
	return segs, nil
}

func findClose(s string, from int) (int, error) {
	var quote byte
	for i := from; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case strings.HasPrefix(s[i:], closeTag):
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: unterminated ${{ expression", ErrCompile)
}

// Interpolate resolves the ${{ }} expressions in s. If s is exactly one
// expression the result keeps its CEL type; otherwise the segments are
// stringified and concatenated and the result is a string.
func (e *Env) Interpolate(s string, vars Vars) (any, error) {
	segs, err := ParseTemplate(s)
	if err != nil {
		return nil, err
	}
	if len(segs) == 1 && segs[0].IsExpr {
		return e.evalSegment(segs[0], vars)
	}
	var b strings.Builder
	for _, seg := range segs {
		if !seg.IsExpr {
			b.WriteString(seg.Literal)
			continue
		}
		v, err := e.evalSegment(seg, vars)
		if err != nil {
			return nil, err
		}
		str, err := stringify(v)
		if err != nil {
			return nil, err
		}
		b.WriteString(str)
	}
	if b.Len() > e.limits.MaxResultBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrResultSize, b.Len(), e.limits.MaxResultBytes)
	}
	return b.String(), nil
}

func (e *Env) evalSegment(seg Segment, vars Vars) (any, error) {
	p, err := e.Compile(seg.Expr)
	if err != nil {
		return nil, fmt.Errorf("${{ %s }}: %w", seg.Expr, err)
	}
	v, err := e.Eval(p, vars)
	if err != nil {
		return nil, fmt.Errorf("${{ %s }}: %w", seg.Expr, err)
	}
	return v, nil
}

// stringify renders a value for concatenation: strings as is, null as empty,
// everything else as compact JSON.
func stringify(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case nil:
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrEval, err)
	}
	return string(b), nil
}

// Resolve walks a JSON-compatible value (as produced by encoding/json into
// any) and interpolates every string. Map keys are not interpolated. The
// input is not modified.
func (e *Env) Resolve(v any, vars Vars) (any, error) {
	switch t := v.(type) {
	case string:
		return e.Interpolate(t, vars)
	case map[string]any:
		out := make(map[string]any, len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic evaluation and error order
		for _, k := range keys {
			r, err := e.Resolve(t[k], vars)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			r, err := e.Resolve(x, vars)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

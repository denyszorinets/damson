// Package expr holds the CEL environments and ${{ }} interpolation used by the
// job spec and the engine (ARCHITECTURE §5.4).
//
// Everything here is a pure function of its inputs: no I/O, no clock, no
// randomness. Programs run under a cost limit and results under a size limit.
// Secret values are never part of the evaluation context; secrets are
// `secret://` strings resolved at dispatch time (§9).
package expr

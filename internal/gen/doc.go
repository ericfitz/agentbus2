// Package gen holds code generated from api/openapi.yaml by make generate.
// gen.go must not be edited; this file, smoke_test.go and openapi31_test.go
// are the only hand-written files in the directory.
//
// Path parameters (Milestone 4 choice 17): the client percent-encodes every
// path parameter exactly once (url.PathEscape: a space is %20, % is %25, ? is
// %3F, # is %23, a comma is %2C, * is %2A; + and : travel as themselves).
// The server decodes once, in the Go 1.22 ServeMux, which matches wildcards
// against the escaped path and hands r.PathValue decoded; the generated
// binder takes that value as already unescaped (ValueIsUnescaped), so nothing
// decodes twice and a literal 100% or a%2Fb round-trips. Hand-written code
// never calls url.PathUnescape on a bound parameter and never builds a path
// by string concatenation.
package gen

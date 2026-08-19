//go:build go1.27

package fuzztape

// This file is a preview of the method spellings that generic methods
// (go1.27) permit, per design/fuzztape-api-alignment.md. Each method is
// a one-line delegation to the portable free function, which remains
// the canonical spelling until the module floor moves to 1.27; then
// this file folds into gen.go and tape.go and the tag comes off.
//
// Machines in this tree must keep using the free functions: a machine
// written against these methods silently stops running under 1.26.

// Draw returns a value of g decoded from the tape. It is g(t) as a
// method, for left-to-right composition.
func (t *Tape) Draw[T any](g Gen[T]) T {
	return g(t)
}

// Pick returns one of opts, chosen by the tape. It is the free
// function Pick as a method.
func (t *Tape) Pick[T any](opts []T) T {
	return Pick(t, opts)
}

// OneOf returns a value drawn from one of gens, chosen by the tape.
func (t *Tape) OneOf[T any](gens ...Gen[T]) T {
	return OneOf(gens...)(t)
}

// Map returns a generator that yields f applied to g's values. It is
// the free function Map as a method.
func (g Gen[A]) Map[B any](f func(A) B) Gen[B] {
	return Map(g, f)
}

package generic

import (
	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	mr "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
)

func configure[T any](set func(*T)) *T {
	value := new(T)
	set(value)
	return value
}

func zeroPointer[T any]() *T { return new(T) }

func slice[T any](n int) []T { return make([]T, n) }

type box[T any] struct{ value T }

func Configured(runtime cf.ModelRuntime) cf.ModelRuntime {
	return configure(func(resolver *mr.Resolver) { resolver.Default = runtime })
}

func Explicit() { _ = zeroPointer[mr.Resolver]() }

func Made() { _ = slice[mr.Resolver](1) }

func Boxed(resolver *box[mr.Resolver]) { _ = resolver }

func PointerOnly() { _ = zeroPointer[*mr.Resolver](); _ = slice[*mr.Resolver](1) }

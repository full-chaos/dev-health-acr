package dotimport

import . "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"

func Dot() { _ = New(nil, nil, nil) }

func DotLiteral() { _ = &Resolver{} }

func DotValue() { construct := New; _ = construct }

func DotZero() { var zero Resolver; _ = zero }

func DotPointer(resolver *Resolver) { _ = (*Resolver)(nil); _ = resolver }

type names struct{ New, Resolver int }

func DotSameNames() {
	value := names{New: 1, Resolver: 2}
	_ = value.New
	{
		New := 3
		_ = New
	}
	goto Resolver
Resolver:
}

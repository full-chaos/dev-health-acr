package aliases

import (
	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	mr "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	res "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
)

func Plain() { _ = modelruntimeresolver.New(nil, nil, nil) }

func Aliased() { _ = mr.New(nil, nil, nil) }

func Literal() { _ = &res.Resolver{} }

func Engine() { _, _ = cf.NewEngine(cf.EngineDependencies{}, cf.EngineOptions{}) }

func Shadowed() {
	mr := struct{ New, Resolver, NewEngine int }{}
	cf := mr
	_, _, _ = mr.New, mr.Resolver, cf.NewEngine
}

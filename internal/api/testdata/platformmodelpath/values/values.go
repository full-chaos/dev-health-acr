package values

import (
	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	. "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	mr "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
)

var packageValue = mr.New

func Value() { construct := mr.New; _ = construct }

func EngineValue() { build := cf.NewEngine; _ = build }

func InvestigateValue(engine *cf.Engine) { run := engine.Investigate; _ = run }

func InvestigateMethodExpression() { _ = (*cf.Engine).Investigate }

func ModelValue(runtime cf.ModelRuntime) { phrase := runtime.SynthesizeAnswer; _ = phrase }

func Zero() { var zero mr.Resolver; zero.Default = nil; _ = &zero }

func Allocated() { _ = new(mr.Resolver) }

func Made() { _ = make([]mr.Resolver, 1) }

func Copied(resolver *mr.Resolver) { copied := *resolver; _ = copied }

func Pointers(resolver *mr.Resolver) (*mr.Resolver, func(*mr.Resolver)) {
	var none *mr.Resolver
	var parenthesized *(mr.Resolver)
	_, _ = any(resolver).(*(mr.Resolver))
	_ = parenthesized
	return none, func(*mr.Resolver) {}
}

func Both() { _ = mr.New(nil, nil, nil); _ = Build(nil) }

type holder struct{ resolver mr.Resolver }

type pointerHolder struct{ resolver *mr.Resolver }

var _ = holder{}
var _ = pointerHolder{}

package dotimport

import . "github.com/full-chaos/dev-health-acr/internal/contextfabric"

func DotEngine() { _, _ = NewEngine(EngineDependencies{}, EngineOptions{}) }

func DotEngineValue() { build := NewEngine; _ = build }

type engineNames struct{ NewEngine int }

func DotEngineSameName() { _ = engineNames{NewEngine: 1}.NewEngine }

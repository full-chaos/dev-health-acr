package elided

import mr "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"

type pointer = *mr.Resolver

func Slice() { _ = []*mr.Resolver{{}} }

func Array() { _ = [1]*mr.Resolver{{}} }

func MapValue() { _ = map[string]*mr.Resolver{"a": {}} }

func MapKey() { _ = map[*mr.Resolver]int{{}: 1} }

func Nested() { _ = [][]*mr.Resolver{{{}}} }

func ThroughAlias() { _ = []pointer{{}} }

func Nils() { _ = []*mr.Resolver{nil}; _ = map[*mr.Resolver]*mr.Resolver{nil: nil} }

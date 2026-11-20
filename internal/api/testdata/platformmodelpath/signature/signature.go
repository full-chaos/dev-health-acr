package signature

import (
	"unsafe"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	mr "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
)

func NamedResult() (resolver mr.Resolver) { return }

func UnnamedResult(resolver *mr.Resolver) mr.Resolver { return *resolver }

func ValueParameter(resolver mr.Resolver) {}

func UnnamedParameter(mr.Resolver) {}

func Variadic(resolvers ...mr.Resolver) {}

func UnnamedVariadic(...mr.Resolver) {}

func Constrained[T mr.Resolver](value T) {}

func ArrayBound(_ [unsafe.Sizeof(mr.New)]byte) {}

func EngineArrayBound(_ [unsafe.Sizeof(cf.NewEngine)]byte) {}

func InvestigateArrayBound(_ [unsafe.Sizeof((*cf.Engine).Investigate)]byte) {}

func PointerSignature(resolver *mr.Resolver) *mr.Resolver { return resolver }

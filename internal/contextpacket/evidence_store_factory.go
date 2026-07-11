package contextpacket

type EvidenceStoreFactory func(ClickHouseRows) (*ClickHouseEvidenceStore, error)

func NewEvidenceStoreFactory(codec *EvidenceIDCodec) EvidenceStoreFactory {
	return NewObservedEvidenceStoreFactory(codec, nil, nil)
}

func NewObservedEvidenceStoreFactory(codec *EvidenceIDCodec, expansion EvidenceExpansionObserver, assembly AssemblyObserver) EvidenceStoreFactory {
	return func(rows ClickHouseRows) (*ClickHouseEvidenceStore, error) {
		if catalog, ok := rows.(*CatalogClickHouseRows); ok {
			catalog.observer = assembly
			if catalog.resolver != nil {
				catalog.resolver.observer = assembly
			}
		}
		return NewClickHouseEvidenceStoreWithOptions(rows, EvidenceStoreOptions{
			Codec: codec, Resolver: NewEvidenceResolver(EvidenceResolverOptions{Observer: expansion}),
		})
	}
}

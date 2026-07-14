package hosted

func productionFactories() componentFactories {
	return componentFactories{
		openPostgres: openPostgres, openClickHouse: openClickHouse,
		newEntitlement: newEntitlement, newEpisode: newEpisode,
	}
}

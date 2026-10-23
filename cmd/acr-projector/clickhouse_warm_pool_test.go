package main

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

func TestProjectorClickHouseOptionsKeepAWarmPool(t *testing.T) {
	ceiling := uint64(1)
	options := projectorClickHouseOptions(config.ProjectorConfig{ClickHouseDSN: "clickhouse://h:9440/db?secure=true"}, nil, &ceiling)
	if options.MaxIdleConns < options.MaxOpenConns || options.MaxOpenConns < 4 {
		t.Fatalf("projector pool open=%d idle=%d: idle must cover open (surplus connections are closed on release) and open must cover 4 concurrent statements", options.MaxOpenConns, options.MaxIdleConns)
	}
	if options.ConnMaxLifetime < 10*time.Minute {
		t.Fatalf("projector ConnMaxLifetime = %s", options.ConnMaxLifetime)
	}
	if options.DSN == "" || options.MaxBytesToRead != &ceiling {
		t.Fatalf("projector options lost DSN or MaxBytesToRead: %+v", options)
	}
}

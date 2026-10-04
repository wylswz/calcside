//go:build wireinject

package main

import (
	"github.com/google/wire"

	"calcside/internal/config"
)

func initializeWorker(cfg config.WorkerConfig) (*workerApp, func(), error) {
	wire.Build(provideNodeID, provideNodeConfig, provideRuntime, provideServer, wire.Struct(new(workerApp), "*"))
	return nil, nil, nil
}

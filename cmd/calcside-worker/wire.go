//go:build wireinject

package main

import (
	"github.com/google/wire"

	"calcside/cmd/calcside-worker/internal/config"
)

func initializeWorker(cfg config.WorkerConfig) (*workerApp, func(), error) {
	wire.Build(provideNodeID, provideNodeConfig, provideRuntime, provideServer, wire.Struct(new(workerApp), "*"))
	return nil, nil, nil
}

//go:build wireinject

package node

import (
	"github.com/google/wire"

	"calcside/internal/instance"
)

func Build(c Config) *Node {
	wire.Build(newRegistry, newEngine, managerOptions, instance.New, wire.Struct(new(Node), "*"))
	return nil
}

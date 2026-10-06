//go:build wireinject

package main

import (
	"context"
	"time"

	"github.com/google/wire"

	"calcside/cmd/calcside/internal/api"
	"calcside/cmd/calcside/internal/audit"
	"calcside/cmd/calcside/internal/config"
	auditsvc "calcside/cmd/calcside/internal/service/audit"
	"calcside/cmd/calcside/internal/service/catalog"
	"calcside/cmd/calcside/internal/service/iam"
	policysvc "calcside/cmd/calcside/internal/service/policy"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/service/vault"
	"calcside/internal/node"
)

func initializeApp(ctx context.Context, cfg config.Config) (*application, func(), error) {
	wire.Build(
		provideStore, provideRecorder, provideCipher, provideLimits,
		provideNodeConfig, provideNode, provideRuntime, provideGlobalPolicies,
		wire.FieldsOf(new(*node.Node), "Registry"),
		wire.Bind(new(sandbox.SecretResolver), new(*vault.Service)),
		wire.Bind(new(sandbox.AuditSink), new(*audit.Recorder)),
		provideSandboxOptions, sandbox.New, vault.New,
		wire.Value(time.Now), iam.New,
		policysvc.New, auditsvc.New, provideExtCatalog, catalog.New,
		provideAuth, provideBasic, provideAnonymous, provideGoogle, provideWeb, provideArtifacts,
		wire.Struct(new(api.Deps), "IAM", "Vault", "Policy", "Audit", "Catalog", "Sandbox", "Auth", "Basic", "Web", "Anonymous", "Previews"),
		provideServer, wire.Struct(new(application), "*"),
	)
	return nil, nil, nil
}

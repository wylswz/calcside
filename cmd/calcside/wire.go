//go:build wireinject

package main

import (
	"context"
	"time"

	"github.com/google/wire"

	"calcside/internal/api"
	"calcside/internal/audit"
	"calcside/internal/config"
	"calcside/internal/node"
	auditsvc "calcside/internal/service/audit"
	"calcside/internal/service/catalog"
	"calcside/internal/service/iam"
	policysvc "calcside/internal/service/policy"
	"calcside/internal/service/sandbox"
	"calcside/internal/service/vault"
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
		policysvc.New, auditsvc.New, catalog.New,
		provideAuth, provideBasic, provideAnonymous, provideGoogle, provideWeb, provideArtifacts,
		wire.Struct(new(api.Deps), "IAM", "Vault", "Policy", "Audit", "Catalog", "Sandbox", "Auth", "Basic", "Web", "Anonymous", "Previews"),
		provideServer, wire.Struct(new(application), "*"),
	)
	return nil, nil, nil
}

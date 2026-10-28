//go:build wireinject

package main

import (
	"context"
	"time"

	"github.com/google/wire"

	"calcside/cmd/calcside/internal/api"
	"calcside/cmd/calcside/internal/audit"
	"calcside/cmd/calcside/internal/config"
	"calcside/cmd/calcside/internal/service/access"
	appsvc "calcside/cmd/calcside/internal/service/application"
	auditsvc "calcside/cmd/calcside/internal/service/audit"
	"calcside/cmd/calcside/internal/service/catalog"
	"calcside/cmd/calcside/internal/service/iam"
	llmprovidersvc "calcside/cmd/calcside/internal/service/llmprovider"
	policysvc "calcside/cmd/calcside/internal/service/policy"
	"calcside/cmd/calcside/internal/service/sandbox"
	"calcside/cmd/calcside/internal/service/vault"
	workspacesvc "calcside/cmd/calcside/internal/service/workspace"
)

func initializeApp(ctx context.Context, cfg config.Config) (*application, func(), error) {
	wire.Build(
		provideStore, provideRecorder, provideCipher, provideLimits,
		provideExtRepo, provideRegistry, provideRuntime, provideGlobalPolicies,
		wire.Bind(new(sandbox.SecretResolver), new(*vault.Service)),
		wire.Bind(new(sandbox.AuditSink), new(*audit.Recorder)),
		wire.Bind(new(appsvc.AuditSink), new(*audit.Recorder)),
		provideSandboxOptions, sandbox.New, vault.New,
		wire.Bind(new(iam.SpecValidator), new(*sandbox.Service)),
		wire.FieldsOf(new(config.Config), "MaxInstancesPerUser"),
		access.New,
		wire.Value(time.Now), iam.New,
		policysvc.New,
		provideExtOptions, llmprovidersvc.New,
		workspacesvc.New, auditsvc.New, provideExtCatalog, catalog.New,
		provideAuth, provideBasic, provideAnonymous, provideGoogle, provideWeb, provideArtifacts,
		wire.Struct(new(api.Deps), "IAM", "Vault", "Policy", "Audit", "Catalog", "Sandbox", "Workspace", "Access", "LLMProvider", "Auth", "Basic", "Web", "Anonymous", "Previews", "Application"),
		provideApplicationService, provideServingService, provideServer, wire.Struct(new(application), "*"),
	)
	return nil, nil, nil
}

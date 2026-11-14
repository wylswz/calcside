package api

import (
	"context"
	"encoding/json"
	"net/http"

	"calcside/cmd/calcside/internal/api/gen"
	"calcside/cmd/calcside/internal/service"
	"calcside/internal/serving"
	"calcside/internal/types"
)

// Contract editor tools: pure, stateless transforms over caller text —
// no store, no scope, no data operation. Any authenticated caller may
// use them; errors are generic and never echo sample text or keys.

const contractTextLimit = 64 << 10

type inferContractResp struct{ rawJSON }

func (r inferContractResp) VisitInferContractResponse(w http.ResponseWriter) error {
	return r.write(w)
}

type validateContractResp struct{ rawJSON }

func (r validateContractResp) VisitValidateContractResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func contractBad() rawJSON {
	return fail(&service.Error{Code: types.ErrCodeBadRequest, Msg: "invalid contract document"})
}

func (s *strictImpl) InferContract(ctx context.Context, req gen.InferContractRequestObject) (gen.InferContractResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return inferContractResp{*e}, nil
	}
	if req.Body == nil || len(req.Body.Sample) > contractTextLimit {
		return inferContractResp{contractBad()}, nil
	}
	out, err := serving.InferSchema([]byte(req.Body.Sample))
	if err != nil {
		return inferContractResp{contractBad()}, nil
	}
	return inferContractResp{rawJSON{http.StatusOK, gen.ContractDocument{Schema: string(out)}}}, nil
}

func (s *strictImpl) ValidateContract(ctx context.Context, req gen.ValidateContractRequestObject) (gen.ValidateContractResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return validateContractResp{*e}, nil
	}
	if req.Body == nil || len(req.Body.Schema) > contractTextLimit {
		return validateContractResp{contractBad()}, nil
	}
	raw := []byte(req.Body.Schema)
	// Decode with the schema compiler's bounds first — malformed or
	// oversized input is a generic 400, never an echo.
	v, err := serving.DecodeJSON(raw, serving.Limits{MaxBytes: contractTextLimit, MaxDepth: 32, MaxNodes: 2048})
	if err != nil {
		return validateContractResp{contractBad()}, nil
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return validateContractResp{contractBad()}, nil
	}
	if _, err := serving.CompileSchema(canonical, serving.Limits{MaxBytes: contractTextLimit, MaxDepth: 32, MaxNodes: 2048}); err != nil {
		return validateContractResp{contractBad()}, nil
	}
	return validateContractResp{rawJSON{http.StatusOK, gen.ContractDocument{Schema: string(canonical)}}}, nil
}

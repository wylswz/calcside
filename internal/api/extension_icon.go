package api

import (
	"context"
	"net/http"

	"calcside/internal/api/gen"
	"calcside/internal/service"
	"calcside/internal/types"
)

type extensionIconResp struct {
	rawJSON
	icon []byte
}

func (r extensionIconResp) VisitExtensionIconResponse(w http.ResponseWriter) error {
	if r.icon == nil {
		return r.rawJSON.write(w)
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, err := w.Write(r.icon)
	return err
}

func (s *strictImpl) ExtensionIcon(ctx context.Context, req gen.ExtensionIconRequestObject) (gen.ExtensionIconResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return extensionIconResp{rawJSON: *e}, nil
	}
	icon := s.d.Catalog.Icon(req.IconId)
	if icon == nil {
		return extensionIconResp{rawJSON: fail(service.Errf(types.ErrCodeNotFound, "icon not found"))}, nil
	}
	return extensionIconResp{icon: icon}, nil
}

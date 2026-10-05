package api

import (
	"context"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"calcside/internal/api/gen"
	"calcside/internal/artifact"
	"calcside/internal/runtime"
	"calcside/internal/service"
	"calcside/internal/types"
)

type artifactConfigResp struct{ rawJSON }

func (r artifactConfigResp) VisitArtifactConfigResponse(w http.ResponseWriter) error {
	artifactHeaders(w.Header())
	return r.write(w)
}

type previewArtifactResp struct{ rawJSON }

func (r previewArtifactResp) VisitPreviewArtifactResponse(w http.ResponseWriter) error {
	artifactHeaders(w.Header())
	return r.write(w)
}

type artifactPreview struct {
	Path            string     `json:"path"`
	Kind            string     `json:"kind"`
	Source          string     `json:"source"`
	SourceTruncated bool       `json:"source_truncated"`
	Redacted        bool       `json:"redacted"`
	Size            int        `json:"size"`
	CSVRows         [][]string `json:"csv_rows,omitempty"`
	CSVTotalRows    int        `json:"csv_total_rows,omitempty"`
	CSVTruncated    bool       `json:"csv_truncated,omitempty"`
	PreviewError    string     `json:"preview_error,omitempty"`
	PreviewURL      string     `json:"preview_url,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	Interactive     bool       `json:"interactive,omitempty"`
	Sanitized       bool       `json:"sanitized,omitempty"`
}

func (s *strictImpl) ArtifactConfig(ctx context.Context, _ gen.ArtifactConfigRequestObject) (gen.ArtifactConfigResponseObject, error) {
	ctx = realCtx(ctx)
	if _, e := needAuth(ctx); e != nil {
		return artifactConfigResp{*e}, nil
	}
	p := s.d.Previews
	return artifactConfigResp{rawJSON{200, map[string]any{
		"html_enabled": p.base != nil, "interactive_enabled": p.base != nil && p.allowScripts,
		"max_file_bytes": runtime.MaxArtifactFileBytes, "max_total_bytes": runtime.MaxArtifactTotalBytes,
		"max_files": runtime.MaxArtifactFiles, "preview_ttl_seconds": artifact.PreviewTTLSeconds,
	}}}, nil
}

func (s *strictImpl) PreviewArtifact(ctx context.Context, req gen.PreviewArtifactRequestObject) (gen.PreviewArtifactResponseObject, error) {
	ctx = realCtx(ctx)
	principal, e := needAuth(ctx)
	if e != nil {
		return previewArtifactResp{*e}, nil
	}
	bad := func(err error) (gen.PreviewArtifactResponseObject, error) { return previewArtifactResp{fail(err)}, nil }
	if req.Body == nil {
		return bad(service.BadRequest("request body is required"))
	}
	mode := "source"
	if req.Body.Mode != nil {
		mode = string(*req.Body.Mode)
	}
	if mode != "source" && mode != "static" && mode != "interactive" {
		return bad(service.BadRequest("invalid preview mode"))
	}
	if mode != "source" && s.d.Previews.base == nil {
		return bad(service.BadRequest("HTML preview requires an isolated preview domain"))
	}
	if mode == "interactive" && !s.d.Previews.allowScripts {
		return bad(service.Forbidden("interactive preview is disabled"))
	}
	actor := actorOf(principal)
	snapshot, err := s.d.Sandbox.Preview(ctx, actor, req.Id, req.Body.Path, mode)
	if err != nil {
		return bad(err)
	}
	if len(snapshot.Files) != 1 {
		return bad(service.BadRequest("select a single file"))
	}
	file := snapshot.Files[0]
	out := artifactPreview{Path: file.Path, Kind: artifact.TextKind(file.Path), Redacted: file.Redacted, Size: len(file.Content)}
	out.Source, out.SourceTruncated = artifact.Prefix(string(file.Content), artifact.MaxSourceBytes)
	if out.Kind == "csv" {
		delimiter := ','
		if strings.EqualFold(path.Ext(file.Path), ".tsv") {
			delimiter = '\t'
		}
		table, err := artifact.CSV(ctx, string(file.Content), delimiter)
		if err != nil {
			out.PreviewError = err.Error()
		} else {
			out.CSVRows = table.Rows
			out.CSVTotalRows = table.TotalRows
			out.CSVTruncated = table.Truncated
		}
	}
	if mode != "source" {
		if out.Kind != "html" {
			return bad(service.BadRequest("rendered preview requires an HTML file"))
		}
		body := snapshot.PreviewHTML
		if body == nil {
			return bad(service.BadRequest("execution node did not produce an inline HTML preview"))
		}
		out.Sanitized = mode == "static"

		previewURL, expires, err := s.d.Previews.create(actor.UserID, req.Id, snapshot.Epoch, body, mode == "interactive", snapshot.ExpiresAt)
		if err != nil {
			return bad(err)
		}
		out.PreviewURL, out.ExpiresAt, out.Interactive = previewURL, &expires, mode == "interactive"
	}
	return previewArtifactResp{rawJSON{200, out}}, nil
}

type exportArtifactsResp struct {
	failure               *rawJSON
	data                  []byte
	filename, contentType string
	redacted              bool
	fileCount             int
}

func (r exportArtifactsResp) VisitExportArtifactsResponse(w http.ResponseWriter) error {
	artifactHeaders(w.Header())
	if r.failure != nil {
		return r.failure.write(w)
	}
	w.Header().Set("Content-Type", r.contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": r.filename}))
	w.Header().Set("Content-Length", strconv.Itoa(len(r.data)))
	w.Header().Set("X-Calcside-Redacted", strconv.FormatBool(r.redacted))
	w.Header().Set("X-Calcside-File-Count", strconv.Itoa(r.fileCount))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	_, err := w.Write(r.data)
	return err
}

func (s *strictImpl) ExportArtifacts(ctx context.Context, req gen.ExportArtifactsRequestObject) (gen.ExportArtifactsResponseObject, error) {
	ctx = realCtx(ctx)
	principal, e := needAuth(ctx)
	if e != nil {
		return exportArtifactsResp{failure: e}, nil
	}
	bad := func(err error) (gen.ExportArtifactsResponseObject, error) {
		r := fail(err)
		return exportArtifactsResp{failure: &r}, nil
	}
	if req.Body == nil {
		return bad(service.BadRequest("request body is required"))
	}
	format := string(req.Body.Format)
	if format != "file" && format != "zip" {
		return bad(service.BadRequest("invalid export format"))
	}
	snapshot, err := s.d.Sandbox.Export(ctx, actorOf(principal), req.Id, req.Body.Paths, format == "zip")
	if err != nil {
		return bad(err)
	}
	out := exportArtifactsResp{filename: "artifacts.zip", contentType: "application/zip"}
	for _, file := range snapshot.Files {
		if !file.IsDir {
			out.fileCount++
		}
		out.redacted = out.redacted || file.Redacted
	}
	if format == "file" {
		if len(snapshot.Files) != 1 || snapshot.Files[0].IsDir {
			return bad(service.BadRequest("select a single file"))
		}
		out.filename = path.Base(snapshot.Files[0].Path)
		out.contentType = "application/octet-stream"
		out.data = snapshot.Files[0].Content
	} else {
		out.data, err = artifact.ZIP(ctx, snapshot.Files)
		if err != nil {
			return bad(service.Errf(types.ErrCodeTooLarge, "archive could not be prepared within export limits"))
		}
	}
	return out, nil
}

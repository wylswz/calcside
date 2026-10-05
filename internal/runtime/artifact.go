package runtime

import "calcside/internal/types"

const MaxArtifactFileBytes = 1 << 20
const MaxArtifactTotalBytes = 4 << 20
const MaxArtifactFiles = 100
const MaxArtifactEntries = 256
const MaxArtifactPathBytes = 1024

type ExportRequest struct {
	PreviewMode string   `json:"preview_mode,omitempty"`
	CheckOnly   bool     `json:"check_only,omitempty"`
	InstanceID  string   `json:"instance_id"`
	Owner       Owner    `json:"owner"`
	Epoch       int64    `json:"epoch,omitempty"`
	Paths       []string `json:"paths"`
	Recursive   bool     `json:"recursive"`
}

type ArtifactFile struct {
	Path     string `json:"path"`
	Content  []byte `json:"content,omitempty"`
	IsDir    bool   `json:"is_dir"`
	Redacted bool   `json:"redacted"`
}

type ArtifactError struct {
	Code    types.APIErrorCode `json:"code"`
	Message string             `json:"message"`
}

func (e *ArtifactError) Error() string { return e.Message }

type ExportResponse struct {
	PreviewHTML []byte         `json:"preview_html,omitempty"`
	Files       []ArtifactFile `json:"files,omitempty"`
	Error       *ArtifactError `json:"error,omitempty"`
	Audit       AuditBatch     `json:"audit"`
}

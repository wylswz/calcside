package artifact

import (
	"bytes"
	"calcside/internal/runtime"
	"calcside/internal/types"
	"context"
	"github.com/microcosm-cc/bluemonday"
	"path"
	"regexp"
	"strings"
)

func TextKind(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".csv", ".tsv":
		return "csv"
	case ".html", ".htm":
		return "html"
	case ".json":
		return "json"
	default:
		return "text"
	}
}

type BoundedBuffer struct {
	data  bytes.Buffer
	limit int
	ctx   context.Context
}

func (b *BoundedBuffer) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > b.limit-b.data.Len() {
		return 0, &runtime.ArtifactError{Code: types.ErrCodeTooLarge, Message: "artifact output exceeds byte limit"}
	}
	return b.data.Write(p)
}

func StaticHTML(ctx context.Context, src []byte) ([]byte, error) {
	if err := CheckHTML(ctx, src); err != nil {
		return nil, err
	}
	p := bluemonday.NewPolicy()
	p.AllowAttrs("id", "class").Globally()
	p.AllowElements("html", "head", "body", "title", "p", "div", "span", "h1", "h2", "h3", "h4", "h5", "h6", "table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "ul", "ol", "li", "pre", "code", "strong", "em", "b", "i", "blockquote", "br", "hr", "section", "article", "dl", "dt", "dd", "small", "sub", "sup")
	p.AllowElements("svg", "g", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path", "text", "tspan")
	p.AllowAttrs("x", "y", "x1", "y1", "x2", "y2", "width", "height", "cx", "cy", "r", "rx", "ry", "viewbox", "stroke-width", "opacity").Matching(regexp.MustCompile(`^[0-9eE+.,% -]{1,256}$`)).OnElements("svg", "g", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path", "text", "tspan")
	p.AllowAttrs("d", "points").Matching(regexp.MustCompile(`^[MmZzLlHhVvCcSsQqTtAa0-9eE+., -]+$`)).OnElements("path", "polygon", "polyline")
	p.AllowAttrs("fill", "stroke").Matching(regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]{1,24})$`)).OnElements("svg", "g", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path", "text", "tspan")
	p.AllowAttrs("colspan", "rowspan").Matching(regexp.MustCompile(`^[1-9][0-9]{0,2}$`)).OnElements("th", "td")
	p.AllowStyles("color", "background-color", "font-size", "font-weight", "font-family", "text-align", "margin", "padding", "border", "border-collapse", "width", "max-width", "height", "display").Globally()
	out := &BoundedBuffer{limit: runtime.MaxArtifactFileBytes, ctx: ctx}
	if err := p.SanitizeReaderToWriter(bytes.NewReader(src), out); err != nil {
		return nil, err
	}
	if err := CheckHTML(ctx, out.data.Bytes()); err != nil {
		return nil, err
	}
	return out.data.Bytes(), nil
}

func NewBoundedBuffer(ctx context.Context, limit int) *BoundedBuffer {
	return &BoundedBuffer{ctx: ctx, limit: limit}
}

func (b *BoundedBuffer) Bytes() []byte { return b.data.Bytes() }

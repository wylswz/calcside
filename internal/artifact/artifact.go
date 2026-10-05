package artifact

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/publicsuffix"

	capfs "calcside/internal/capability/fs"
	"calcside/internal/runtime"
	"calcside/internal/stdlib"
)

const PreviewTTLSeconds = 120
const MaxSourceBytes = 64 << 10
const MaxPreviewRows = 200
const MaxPreviewColumns = 32
const MaxPreviewCellBytes = 512
const MaxPreviewTableBytes = 256 << 10
const MaxArchiveBytes = 8 << 20

type Config struct {
	BaseURL        string
	ConsoleOrigin  string
	AllowScripts   bool
	AllowLocalHTTP bool
}

var domainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func (c Config) Origins() (*url.URL, string, error) {
	if c.BaseURL == "" {
		if c.AllowScripts {
			return nil, "", errors.New("artifact scripts require an isolated preview base URL")
		}
		return nil, "", nil
	}
	origin := func(raw string) (*url.URL, error) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			return nil, errors.New("artifact configuration requires an HTTP(S) origin without path, credentials, query, or fragment")
		}
		u.Host = strings.ToLower(u.Host)
		h := u.Hostname()
		local := h == "localhost" || strings.HasSuffix(h, ".localhost")
		if u.Scheme != "https" && !(c.AllowLocalHTTP && u.Scheme == "http" && local) {
			return nil, errors.New("artifact origins require HTTPS; dev HTTP is limited to localhost domains")
		}
		if strings.HasSuffix(h, ".") || net.ParseIP(h) != nil {
			return nil, errors.New("artifact origins require canonical DNS hosts")
		}
		if h != "localhost" && (len(h) > 253 || !domainPattern.MatchString(h)) {
			return nil, errors.New("invalid artifact or console host")
		}
		if port := u.Port(); port != "" {
			if value, err := strconv.ParseUint(port, 10, 16); err != nil || value == 0 {
				return nil, errors.New("invalid artifact or console port")
			}
		}
		if strings.HasSuffix(u.Host, ":") {
			return nil, errors.New("invalid artifact or console port")
		}
		u.Path = ""
		return u, nil
	}
	preview, err := origin(c.BaseURL)
	if err != nil {
		return nil, "", err
	}
	console, err := origin(c.ConsoleOrigin)
	if err != nil {
		return nil, "", err
	}
	ph, ch := preview.Hostname(), console.Hostname()
	if len(ph) > 210 || !domainPattern.MatchString(ph) {
		return nil, "", errors.New("invalid artifact preview domain")
	}
	local := c.AllowLocalHTTP && strings.HasSuffix(ph, ".localhost") && (ch == "localhost" || strings.HasSuffix(ch, ".localhost"))
	if ph == ch || !local && (strings.HasSuffix(ch, "."+ph) || strings.HasSuffix(ph, "."+ch)) {
		return nil, "", errors.New("artifact and console domains must be independent")
	}
	if !(c.AllowLocalHTTP && local) {
		ps, pe := publicsuffix.EffectiveTLDPlusOne(ph)
		cs, ce := publicsuffix.EffectiveTLDPlusOne(ch)
		if pe != nil || ce != nil || ps == cs {
			return nil, "", errors.New("artifact and console must use different registrable domains")
		}
	}
	return preview, console.String(), nil
}

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

func Prefix(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max], true
}

type Table struct {
	Rows      [][]string
	TotalRows int
	Truncated bool
}

func CSV(ctx context.Context, s string, delimiter rune) (Table, error) {
	out := Table{Rows: [][]string{}}
	if err := stdlib.CheckCSVLimits(ctx, s, delimiter); err != nil {
		return out, err
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(s, "\ufeff")))
	reader.Comma = delimiter
	remaining := MaxPreviewTableBytes
	for {
		if err := ctx.Err(); err != nil {
			return Table{}, err
		}
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			var p *csv.ParseError
			if errors.As(err, &p) {
				return Table{}, fmt.Errorf("invalid CSV at line %d column %d", p.Line, p.Column)
			}
			return Table{}, errors.New("invalid CSV")
		}
		out.TotalRows++
		if len(out.Rows) >= MaxPreviewRows || remaining == 0 {
			out.Truncated = true
			continue
		}
		if len(row) > MaxPreviewColumns {
			row = row[:MaxPreviewColumns]
			out.Truncated = true
		}
		visible := make([]string, len(row))
		for i, cell := range row {
			max := min(MaxPreviewCellBytes, remaining)
			value, truncated := Prefix(cell, max)
			visible[i] = strings.Clone(value)
			remaining -= len(value)
			out.Truncated = out.Truncated || truncated
		}
		out.Rows = append(out.Rows, visible)
	}
	return out, nil
}

type boundedBuffer struct {
	data  bytes.Buffer
	limit int
	ctx   context.Context
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > b.limit-b.data.Len() {
		return 0, errors.New("artifact output exceeds byte limit")
	}
	return b.data.Write(p)
}

func StaticHTML(ctx context.Context, src []byte) ([]byte, error) {
	if err := CheckHTML(ctx, src); err != nil {
		return nil, err
	}
	p := bluemonday.NewPolicy()
	p.AllowElements("html", "head", "body", "title", "p", "div", "span", "h1", "h2", "h3", "h4", "h5", "h6", "table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "ul", "ol", "li", "pre", "code", "strong", "em", "b", "i", "blockquote", "br", "hr", "section", "article", "dl", "dt", "dd", "small", "sub", "sup")
	p.AllowElements("svg", "g", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path", "text", "tspan")
	p.AllowAttrs("x", "y", "x1", "y1", "x2", "y2", "width", "height", "cx", "cy", "r", "rx", "ry", "viewbox", "stroke-width", "opacity").Matching(regexp.MustCompile(`^[0-9eE+.,% -]{1,256}$`)).OnElements("svg", "g", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path", "text", "tspan")
	p.AllowAttrs("d", "points").Matching(regexp.MustCompile(`^[MmZzLlHhVvCcSsQqTtAa0-9eE+., -]+$`)).OnElements("path", "polygon", "polyline")
	p.AllowAttrs("fill", "stroke").Matching(regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]{1,24})$`)).OnElements("svg", "g", "rect", "circle", "ellipse", "line", "polyline", "polygon", "path", "text", "tspan")
	p.AllowAttrs("colspan", "rowspan").Matching(regexp.MustCompile(`^[1-9][0-9]{0,2}$`)).OnElements("th", "td")
	p.AllowStyles("color", "background-color", "font-size", "font-weight", "font-family", "text-align", "margin", "padding", "border", "border-collapse", "width", "max-width", "height", "display").Globally()
	out := &boundedBuffer{limit: runtime.MaxArtifactFileBytes, ctx: ctx}
	if err := p.SanitizeReaderToWriter(bytes.NewReader(src), out); err != nil {
		return nil, err
	}
	if err := CheckHTML(ctx, out.data.Bytes()); err != nil {
		return nil, err
	}
	return out.data.Bytes(), nil
}

func ZIP(ctx context.Context, files []runtime.ArtifactFile) ([]byte, error) {
	out := &boundedBuffer{limit: MaxArchiveBytes, ctx: ctx}
	writer := zip.NewWriter(out)
	portableNames := map[string]string{}
	seen := map[string]bool{}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(file.Path) > runtime.MaxArtifactPathBytes || seen[file.Path] {
			return nil, errors.New("invalid or duplicate ZIP entry path")
		}
		if err := capfs.CheckExportPath(file.Path, portableNames); err != nil {
			return nil, err
		}
		seen[file.Path] = true
		name := strings.TrimPrefix(file.Path, "/work/")
		if name == file.Path || name == "" || !filepathSafe(name) {
			return nil, errors.New("invalid ZIP entry path")
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		if file.IsDir {
			header.Name += "/"
			header.SetMode(fs.ModeDir | 0o755)
			header.Method = zip.Store
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(file.Content); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.data.Bytes(), nil
}

func filepathSafe(name string) bool {
	return path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../") && !strings.HasPrefix(name, "/") && !strings.ContainsAny(name, "\\:\x00\r\n")
}

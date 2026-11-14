package artifact

import (
	"archive/zip"
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

	"golang.org/x/net/publicsuffix"

	shared "calcside/internal/artifact"
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

func ZIP(ctx context.Context, files []runtime.ArtifactFile) ([]byte, error) {
	out := shared.NewBoundedBuffer(ctx, MaxArchiveBytes)
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
	return out.Bytes(), nil
}

func filepathSafe(name string) bool {
	return path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../") && !strings.HasPrefix(name, "/") && !strings.ContainsAny(name, "\\:\x00\r\n")
}

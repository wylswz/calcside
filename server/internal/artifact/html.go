package artifact

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"path"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/evanw/esbuild/pkg/api"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"calcside/internal/runtime"
	"calcside/internal/types"
)

func CheckHTML(ctx context.Context, src []byte) error {
	return checkHTML(ctx, src, 64<<10)
}

func checkHTML(ctx context.Context, src []byte, maxTag int) error {
	if len(src) > runtime.MaxArtifactFileBytes {
		return errors.New("HTML exceeds byte limit")
	}
	tokenizer := html.NewTokenizer(bytes.NewReader(src))
	tokenizer.SetMaxBuf(runtime.MaxArtifactFileBytes)
	stack := []string{}
	tokens, attrs := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			if tokenizer.Err() == io.EOF {
				return nil
			}
			return errors.New("HTML token exceeds limit")
		}
		tokens++
		if tokens > 8192 {
			return errors.New("HTML markup exceeds token limit")
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken && kind != html.EndTagToken {
			continue
		}
		if len(tokenizer.Raw()) > maxTag {
			return errors.New("HTML tag exceeds byte limit")
		}
		name, more := tokenizer.TagName()
		tag := string(name)
		count := 0
		for more {
			key, value, next := tokenizer.TagAttr()
			more = next
			if (string(key) == "d" || string(key) == "points") && len(value) > 8192 {
				return errors.New("SVG geometry exceeds limit")
			}
			count++
			attrs++
			if count > 64 || attrs > 16384 {
				return errors.New("HTML attributes exceed limit")
			}
		}
		if kind == html.EndTagToken {
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == tag {
					stack = stack[:i]
					break
				}
			}
			continue
		}
		if strings.Contains("|area|base|br|col|embed|hr|img|input|link|meta|param|source|track|wbr|", "|"+tag+"|") {
			continue
		}
		foreign := tag == "svg" || tag == "math"
		for _, ancestor := range stack {
			foreign = foreign || ancestor == "svg" || ancestor == "math"
		}
		if kind == html.SelfClosingTagToken && foreign {
			continue
		}
		stack = append(stack, tag)
		if len(stack) > 64 {
			return errors.New("HTML nesting exceeds limit")
		}
	}
}

func inlineError(message string) error {
	return &runtime.ArtifactError{Code: types.ErrCodeBadRequest, Message: message}
}

type htmlInliner struct {
	ctx     context.Context
	root    string
	read    func(string) ([]byte, error)
	mu      sync.Mutex
	readErr error
}

func (in *htmlInliner) resolve(importer, ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Path == "" || strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "\\") || !utf8.ValidString(u.Path) || strings.IndexFunc(u.Path, unicode.IsControl) >= 0 {
		return "", inlineError("preview resources must use local relative URLs")
	}
	full := path.Join(path.Dir(importer), u.Path)
	if !strings.HasPrefix(full, in.root+"/") || len(full) > runtime.MaxArtifactPathBytes {
		return "", inlineError("preview resource is outside the HTML entry directory")
	}
	return full, nil
}

func (in *htmlInliner) load(name string) ([]byte, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.readErr != nil {
		return nil, in.readErr
	}
	if err := in.ctx.Err(); err != nil {
		return nil, err
	}
	body, err := in.read(name)
	if err != nil {
		in.readErr = err
	}
	return body, err
}

func inlineLoader(name string) api.Loader {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".mjs":
		return api.LoaderJS
	case ".css":
		return api.LoaderCSS
	case ".json":
		return api.LoaderJSON
	default:
		return api.LoaderNone
	}
}

func (in *htmlInliner) prepareModule(name string, body []byte) ([]byte, error) {
	if err := in.ctx.Err(); err != nil {
		return nil, err
	}
	result := api.Transform(string(body), api.TransformOptions{
		Loader: api.LoaderJS, Format: api.FormatESModule, Sourcefile: name,
		LogLevel: api.LogLevelSilent, LegalComments: api.LegalCommentsNone, Charset: api.CharsetUTF8,
		LogOverride: map[string]api.LogLevel{"unsupported-dynamic-import": api.LogLevelError, "unsupported-require-call": api.LogLevelError},
	})
	if err := in.ctx.Err(); err != nil {
		return nil, err
	}
	if len(result.Errors) != 0 || len(result.Warnings) != 0 {
		return nil, inlineError("module syntax or non-literal imports are not supported in inline previews")
	}
	if len(result.Code) > runtime.MaxArtifactFileBytes {
		return nil, &runtime.ArtifactError{Code: types.ErrCodeTooLarge, Message: "compiled module exceeds byte limit"}
	}
	return result.Code, nil
}

func (in *htmlInliner) compile(name string, body []byte, loader api.Loader, module bool) ([]byte, []byte, error) {
	if err := in.ctx.Err(); err != nil {
		return nil, nil, err
	}
	if module {
		prepared, err := in.prepareModule(name, body)
		if err != nil {
			return nil, nil, err
		}
		body = prepared
	}
	plugin := api.Plugin{Name: "calcside-vfs", Setup: func(build api.PluginBuild) {
		build.OnStart(func() (api.OnStartResult, error) { return api.OnStartResult{}, in.ctx.Err() })
		build.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
			if args.Kind == api.ResolveCSSURLToken && (strings.HasPrefix(args.Path, "data:") || strings.HasPrefix(args.Path, "#")) {
				return api.OnResolveResult{Path: args.Path, External: true}, nil
			}
			if args.Kind == api.ResolveCSSURLToken {
				return api.OnResolveResult{}, inlineError("local CSS images and fonts are not supported; use embedded data URLs")
			}
			if args.Kind != api.ResolveCSSImportRule && args.Kind != api.ResolveCSSComposesFrom && !strings.HasPrefix(args.Path, "./") && !strings.HasPrefix(args.Path, "../") {
				return api.OnResolveResult{}, inlineError("module imports must be relative VFS paths")
			}
			full, err := in.resolve(args.Importer, args.Path)
			if err != nil {
				return api.OnResolveResult{}, err
			}
			kind := inlineLoader(full)
			if kind == api.LoaderNone || (args.Kind == api.ResolveCSSImportRule || args.Kind == api.ResolveCSSComposesFrom) && kind != api.LoaderCSS {
				return api.OnResolveResult{}, inlineError("unsupported preview dependency type")
			}
			if len(args.With) != 0 && (len(args.With) != 1 || args.With["type"] != "json" || kind != api.LoaderJSON) {
				return api.OnResolveResult{}, inlineError("unsupported module import attributes")
			}
			return api.OnResolveResult{Path: full, Namespace: "calcside-vfs"}, nil
		})
		build.OnLoad(api.OnLoadOptions{Filter: ".*"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
			if args.Namespace != "calcside-vfs" {
				return api.OnLoadResult{}, inlineError("host filesystem loading is disabled for previews")
			}
			body, err := in.load(args.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			if inlineLoader(args.Path) == api.LoaderJS {
				body, err = in.prepareModule(args.Path, body)
				if err != nil {
					return api.OnLoadResult{}, err
				}
			}
			content := string(body)
			return api.OnLoadResult{Contents: &content, Loader: inlineLoader(args.Path)}, nil
		})
	}}
	options := api.BuildOptions{
		Stdin:   &api.StdinOptions{Contents: string(body), Sourcefile: name, Loader: loader},
		Plugins: []api.Plugin{plugin}, Bundle: module || loader == api.LoaderCSS,
		Write: false, AbsWorkingDir: "/", Outfile: "/preview.js", TsconfigRaw: "{}",
		Platform: api.PlatformNeutral, LogLevel: api.LogLevelSilent, LegalComments: api.LegalCommentsNone,
		Charset: api.CharsetUTF8, Supported: map[string]bool{"inline-script": true, "inline-style": true},
		LogOverride: map[string]api.LogLevel{"unsupported-dynamic-import": api.LogLevelError, "unsupported-require-call": api.LogLevelError},
	}
	if loader == api.LoaderCSS {
		options.Outfile = "/preview.css"
	}
	if module {
		options.Format = api.FormatESModule
	}
	build, err := api.Context(options)
	if err != nil {
		return nil, nil, inlineError("could not initialize preview compiler")
	}
	defer build.Dispose()
	stop := context.AfterFunc(in.ctx, build.Cancel)
	defer stop()
	result := build.Rebuild()
	if err := in.ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(result.Errors) != 0 || len(result.Warnings) != 0 {
		if in.readErr != nil {
			return nil, nil, in.readErr
		}
		for _, message := range result.Errors {
			if err, ok := message.Detail.(*runtime.ArtifactError); ok {
				return nil, nil, err
			}
		}
		return nil, nil, inlineError("preview JS/CSS could not be compiled; check syntax and local dependencies")
	}
	var script, style []byte
	for _, file := range result.OutputFiles {
		if len(file.Contents) > runtime.MaxArtifactFileBytes {
			return nil, nil, &runtime.ArtifactError{Code: types.ErrCodeTooLarge, Message: "compiled preview exceeds byte limit"}
		}
		switch path.Ext(file.Path) {
		case ".css":
			style = file.Contents
		case ".js":
			script = file.Contents
		default:
			return nil, nil, inlineError("preview compiler produced an external asset")
		}
	}
	return script, style, nil
}

func htmlAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val
		}
	}
	return ""
}

func replaceHTMLText(n *html.Node, text string) {
	for n.FirstChild != nil {
		n.RemoveChild(n.FirstChild)
	}
	n.AppendChild(&html.Node{Type: html.TextNode, Data: text})
}

func InlineHTML(ctx context.Context, entry string, src []byte, read func(string) ([]byte, error), interactive bool) ([]byte, error) {
	if TextKind(entry) != "html" || path.Clean(entry) != entry || !strings.HasPrefix(entry, "/work/") {
		return nil, inlineError("invalid HTML preview entry")
	}
	if err := CheckHTML(ctx, src); err != nil {
		return nil, inlineError("HTML cannot be rendered within preview limits")
	}
	doc, err := html.Parse(bytes.NewReader(src))
	if err != nil {
		return nil, inlineError("HTML could not be parsed")
	}
	in := htmlInliner{ctx: ctx, root: path.Dir(entry), read: read}
	var styles []*html.Node
	modules, compiledBytes := 0, 0
	var visit func(*html.Node) error
	visit = func(n *html.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.Type == html.ElementNode && n.Namespace == "" {
			if n.Data == "template" {
				return nil
			}
			ref, loader, module := "", api.LoaderNone, false
			switch n.Data {
			case "link":
				if strings.EqualFold(htmlAttr(n, "rel"), "stylesheet") {
					ref, loader = htmlAttr(n, "href"), api.LoaderCSS
				}
			case "style":
				loader = api.LoaderCSS
			case "script":
				kind := strings.ToLower(strings.TrimSpace(htmlAttr(n, "type")))
				if interactive && kind == "importmap" {
					return inlineError("import maps are not supported in inline previews")
				}
				if interactive && (kind == "" || kind == "module" || kind == "text/javascript" || kind == "application/javascript") {
					ref, module = htmlAttr(n, "src"), kind == "module"
					if ref != "" || module {
						loader = api.LoaderJS
					}
				}
			}
			if loader != api.LoaderNone {
				if module {
					modules++
					if modules > 1 {
						return inlineError("inline previews support one module entry per HTML document")
					}
				}
				if htmlAttr(n, "integrity") != "" {
					return inlineError("integrity-checked resources cannot be rewritten for preview")
				}
				name, content := entry, []byte(nil)
				if ref != "" {
					name, err = in.resolve(entry, ref)
					if err != nil {
						return err
					}
					if inlineLoader(name) != loader {
						return inlineError("preview resource extension does not match its HTML tag")
					}
					content, err = in.load(name)
					if err != nil {
						return err
					}
				} else {
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						content = append(content, c.Data...)
					}
				}
				js, css, err := in.compile(name, content, loader, module)
				if err != nil {
					return err
				}
				compiledBytes += len(js) + len(css)
				if compiledBytes > runtime.MaxArtifactFileBytes {
					return &runtime.ArtifactError{Code: types.ErrCodeTooLarge, Message: "combined preview exceeds byte limit"}
				}
				if loader == api.LoaderCSS {
					n.Data, n.DataAtom = "style", atom.Style
					attrs := []html.Attribute{}
					for _, a := range n.Attr {
						if a.Key == "media" || a.Key == "title" || a.Key == "id" || a.Key == "class" {
							attrs = append(attrs, a)
						}
					}
					n.Attr = attrs
					replaceHTMLText(n, string(css))
					styles = append(styles, n)
				} else {
					attrs := []html.Attribute{}
					for _, a := range n.Attr {
						if a.Key != "src" {
							attrs = append(attrs, a)
						}
					}
					n.Attr = append(attrs, html.Attribute{Key: "src", Val: "data:text/javascript;charset=utf-8;base64," + base64.StdEncoding.EncodeToString(js)})
					replaceHTMLText(n, "")
					if len(css) != 0 {
						style := &html.Node{Type: html.ElementNode, Data: "style", DataAtom: atom.Style}
						replaceHTMLText(style, string(css))
						n.Parent.InsertBefore(style, n)
						styles = append(styles, style)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if err := visit(c); err != nil {
				return err
			}
			c = next
		}
		return nil
	}
	if err := visit(doc); err != nil {
		return nil, err
	}
	out := &BoundedBuffer{limit: runtime.MaxArtifactFileBytes, ctx: ctx}
	if !interactive {
		for _, style := range styles {
			style.Parent.RemoveChild(style)
		}
		if err := html.Render(out, doc); err != nil {
			return nil, err
		}
		clean, err := StaticHTML(ctx, out.data.Bytes())
		if err != nil {
			return nil, err
		}
		out.data.Reset()
		for _, style := range styles {
			if err := html.Render(out, style); err != nil {
				return nil, err
			}
		}
		if _, err := out.Write(clean); err != nil {
			return nil, err
		}
	} else if err := html.Render(out, doc); err != nil {
		return nil, err
	}
	if err := checkHTML(ctx, out.data.Bytes(), runtime.MaxArtifactFileBytes); err != nil {
		return nil, inlineError("converted HTML exceeds preview limits")
	}
	return out.data.Bytes(), nil
}

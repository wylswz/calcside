package artifact

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"golang.org/x/net/html"

	"calcside/internal/runtime"
)

func CheckHTML(ctx context.Context, src []byte) error {
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
		if len(tokenizer.Raw()) > 64<<10 {
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

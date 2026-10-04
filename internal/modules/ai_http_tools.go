//go:build qg_http_tools && (qg_openai || qg_anthropic)

package modules

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

func init() {
	aiTools["http"] = aiHTTPTool
	core.Register("http_tools", func(*core.Core) error { return nil })
}
func aiHTTPTool(ctx context.Context, tool config.Tool, args json.RawMessage, request aiRequester) (string, error) {
	if len(tool.Methods) != 1 {
		return "", errors.New("HTTP tool must specify exactly one method")
	}
	if !json.Valid(args) {
		return "", errors.New("tool arguments invalid")
	}
	headers := map[string]string{":method": tool.Methods[0]}
	if tool.SecretEnv != "" {
		secret := os.Getenv(tool.SecretEnv)
		if secret == "" {
			return "", errors.New("tool credential unavailable")
		}
		headers["Authorization"] = "Bearer " + secret
	}
	data, e := request(ctx, tool.URL, headers, args, tool.AllowPrivate)
	if e != nil {
		return "", e
	}
	if len(data) > 65536 {
		return "", errors.New("tool response too large")
	}
	return string(data), nil
}

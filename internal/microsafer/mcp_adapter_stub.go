//go:build qg_ai_endpoint && qg_e2ee && !qg_mcp

package microsafer

import "errors"

func (*Runtime) makeMCPHandler(name string, _ HandlerConfig, _ bool) (Handler, error) {
	return Handler{}, errors.New("microsafer: MCP handler requires qg_mcp build feature")
}

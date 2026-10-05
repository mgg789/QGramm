//go:build qg_ai_endpoint && qg_e2ee && !qg_http_tools

package microsafer

import "errors"

func (*Runtime) makeHTTPHandler(name string, _ HandlerConfig, _ bool) (Handler, error) {
	return Handler{}, errors.New("microsafer: HTTP handler requires qg_http_tools build feature")
}

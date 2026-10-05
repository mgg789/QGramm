//go:build qg_ai_endpoint && qg_e2ee && !qg_openai

package microsafer

import "errors"

func (*Runtime) makeModelHandler(name string, _ HandlerConfig, _ bool) (Handler, error) {
	return Handler{}, errors.New("microsafer: model handler requires qg_openai build feature")
}

//go:build qg_ai_endpoint && qg_e2ee && qg_openai

package microsafer

import (
	"context"
	"fmt"
)

func (r *Runtime) makeModelHandler(name string, hc HandlerConfig, require bool) (Handler, error) {
	modelName := hc.Model
	if _, ok := r.cfg.Models[modelName]; !ok {
		return Handler{}, fmt.Errorf("microsafer: handler %q references unknown model %q", name, modelName)
	}
	return Handler{Name: name, RequireApproval: require, StreamRPC: func(ctx context.Context, req RPCRequest, emit ModelResponseFunc) error {
		var in ChatCompletionRequest
		if err := decodeStrictJSON(req.Body, &in); err != nil {
			return fmt.Errorf("microsafer: model request: %w", err)
		}
		return r.completeModelStream(ctx, modelName, in, 1, false, func(resp RPCResponse) error {
			resp.RequestID = req.RequestID
			return emit(resp)
		})
	}}, nil
}

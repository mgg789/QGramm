//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// RegisterConfiguredHandlers binds every configured provider/tool handler to
// a fixed deployment URL. RPC bodies can select a name/action but can never
// select a destination or model.
func (r *Runtime) RegisterConfiguredHandlers() error {
	for name, hc := range r.cfg.Handlers {
		kind := strings.ToLower(strings.TrimSpace(hc.Kind))
		if kind == "" {
			kind = "http"
		}
		require := true
		if h, ok := r.handlers[name]; ok {
			require = h.RequireApproval
		}
		if hc.ApprovalSet {
			require = hc.RequireApproval
		}
		switch kind {
		case "storage":
			// Attached by the qg_ai_storage sidecar after OpenRuntime.
			if h, ok := r.handlers[name]; ok && (h.Fn != nil || h.FnRPC != nil || h.StreamRPC != nil) {
				continue
			}
			continue
		case "model", "llm":
			h, err := r.makeModelHandler(name, hc, require)
			if err != nil {
				return err
			}
			r.handlers[name] = h
		case "http":
			h, err := r.makeHTTPHandler(name, hc, require)
			if err != nil {
				return err
			}
			r.handlers[name] = h
		case "mcp", "tool":
			h, err := r.makeMCPHandler(name, hc, require)
			if err != nil {
				return err
			}
			r.handlers[name] = h
		default:
			return fmt.Errorf("microsafer: handler %q has unsupported kind %q", name, hc.Kind)
		}
	}
	return nil
}

func decodeStrictJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

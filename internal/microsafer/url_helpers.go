//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import "net/url"

func safeNoticeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

//go:build qg_groups

package modules

import (
	"github.com/mgg789/QGramm/internal/core"
	"net/http"
)

func init() {
	core.Register("groups", func(c *core.Core) error {
		c.AddManagementRoute("POST /management/v1/chats/groups", func(w http.ResponseWriter, r *http.Request) { c.CreateChat(w, r, "group") })
		return nil
	})
}

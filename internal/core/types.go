package core

import (
	"context"
	"database/sql"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"net/http"
	"sync"
)

type Identity struct {
	UserID   string `json:"user_id"`
	DeviceID string `json:"device_id"`
}
type MessageInput struct {
	OperationID string              `json:"operation_id"`
	Envelope    *cryptoenc.Envelope `json:"envelope,omitempty"`
	MLS         string              `json:"mls,omitempty"`
	Epoch       int64               `json:"epoch,omitempty"`
	Attachments []string            `json:"attachments,omitempty"`
	ReplyTo     string              `json:"reply_to,omitempty"`
	ForwardFrom string              `json:"forward_from,omitempty"`
	Payload     []byte              `json:"-"`
}
type Message struct {
	ID          string              `json:"id"`
	ChatID      string              `json:"chat_id"`
	Sender      string              `json:"sender"`
	DeviceID    string              `json:"device_id"`
	OperationID string              `json:"operation_id"`
	Seq         int64               `json:"seq"`
	Revision    int                 `json:"revision"`
	Deleted     bool                `json:"deleted"`
	Mode        string              `json:"mode"`
	Epoch       int64               `json:"epoch,omitempty"`
	CreatedAt   int64               `json:"created_at"`
	Envelope    *cryptoenc.Envelope `json:"envelope,omitempty"`
	MLS         string              `json:"mls,omitempty"`
	Attachments []string            `json:"attachments,omitempty"`
	ReplyTo     string              `json:"reply_to,omitempty"`
	ForwardFrom string              `json:"forward_from,omitempty"`
}
type Event struct {
	Seq       int64  `json:"seq"`
	ChatID    string `json:"chat_id"`
	Type      string `json:"type"`
	MessageID string `json:"message_id,omitempty"`
	Data      any    `json:"data,omitempty"`
}
type Handler func(http.ResponseWriter, *http.Request, Identity)
type Installer func(*Core) error

var registry = map[string]Installer{}

func Register(name string, install Installer) {
	if _, ok := registry[name]; ok {
		panic("duplicate module " + name)
	}
	registry[name] = install
}
func Registered() []string {
	out := []string{}
	for name := range registry {
		out = append(out, name)
	}
	return out
}

type Core struct {
	httpSlots        chan struct{}
	DB               *sql.DB
	Config           config.Config
	Engine           *cryptoenc.Engine
	Mux              *http.ServeMux
	Context          context.Context
	cancel           context.CancelFunc
	managementSecret string
	verifyKey        []byte
	mu               sync.Mutex
	connections      map[string]map[*connection]struct{}
	active           int
	Prepare          []func(context.Context, Identity, string, *MessageInput) error
	InTransaction    []func(context.Context, *sql.Tx, Identity, string, Message) error
	Project          []func(context.Context, Identity, *Message) error
	Cleanup          []func(context.Context) error
}

func (c *Core) AddRoute(pattern string, h Handler) { c.Mux.HandleFunc(pattern, c.authorize(h)) }
func (c *Core) AddManagementRoute(pattern string, h http.HandlerFunc) {
	c.Mux.HandleFunc(pattern, c.management(h))
}

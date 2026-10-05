package main

import (
	"context"
	"time"
)

// All adapters use disjoint conversations, one sender and Fanout recipients.
// Extra users are connected idle clients, not additional subscribers.
type Config struct {
	Service, URL, EnvFile, Secret, ResponseMode string
	Users, Chats, Fanout, PayloadBytes, Workers int
	Steps                                       []Step
	Idle, Drain                                 time.Duration
	ReconnectCount                              int
	Offline                                     time.Duration
	PhaseFile                                   string
	FileBytes                                   int64
	FileCount                                   int
	NATSConsumerMemory                          bool
}
type Step struct {
	Name     string
	Rate     int
	Duration time.Duration
}
type Publication struct {
	ID           string
	Chat, Sender int
	Payload      []byte
}
type Delivery struct {
	ID             string
	Chat, Receiver int
	Seq            uint64
	Payload        []byte
}
type Receiver func(Delivery)
type HistoryResult struct {
	Checked    int  `json:"checked"`
	Missing    int  `json:"missing"`
	Unexpected int  `json:"unexpected"`
	Corrupt    int  `json:"corrupt"`
	Supported  bool `json:"supported"`
}
type Adapter interface {
	Send(context.Context, Publication) error
	History(context.Context, []Publication) (HistoryResult, error)
	Disconnect(context.Context, []int) error
	Reconnect(context.Context, []int) error
	Close() error
	Errors() int64
}

// Rejections distinguish bounded server backpressure from uncertain timeouts.
type SendError struct {
	Status       int
	Backpressure bool
	Detail       string
}

func (e *SendError) Error() string { return e.Detail }

func senderFor(c Config, chat int) int   { return chat * (c.Fanout + 1) }
func chatFor(c Config, receiver int) int { return receiver / (c.Fanout + 1) }
func isReceiver(c Config, receiver int) bool {
	return receiver >= 0 && receiver < c.Chats*(c.Fanout+1) && receiver%(c.Fanout+1) != 0
}

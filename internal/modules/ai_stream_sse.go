//go:build qg_ai_streaming && (qg_openai || qg_anthropic)

package modules

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// aiSSEEvent is one complete server-sent event. SSE permits multiple data
// lines per event; they are joined with newlines as required by the format.
type aiSSEEvent struct {
	Event string
	Data  string
}

// aiReadSSE incrementally parses an SSE body. It requires the provider parser
// to explicitly mark a terminal event; EOF alone is always treated as a
// truncated provider response. The body is bounded before parsing, so a
// malicious line or endless stream cannot grow memory without limit.
func aiReadSSE(ctx context.Context, body io.Reader, max int, onEvent func(aiSSEEvent) (bool, error)) error {
	if max <= 0 {
		return errors.New("invalid stream response limit")
	}
	limited := &io.LimitedReader{R: body, N: int64(max) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, min(4096, max)), max)
	var eventName string
	var data strings.Builder
	dispatch := func() (bool, error) {
		if data.Len() == 0 {
			eventName = ""
			return false, nil
		}
		if !utf8.ValidString(data.String()) {
			eventName = ""
			data.Reset()
			return false, errors.New("invalid UTF-8 stream event")
		}
		done, err := onEvent(aiSSEEvent{Event: eventName, Data: data.String()})
		eventName = ""
		data.Reset()
		return done, err
	}
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			done, err := dispatch()
			if err != nil {
				return err
			}
			if done {
				if limited.N == 0 {
					return errors.New("external response too large")
				}
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if ok && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			eventName = value
		case "data":
			if data.Len()+len(value)+1 > max {
				return errors.New("external response too large")
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
	if err := scanner.Err(); err != nil {
		if limited.N == 0 {
			return errors.New("external response too large")
		}
		return errors.New("stream read failed")
	}
	if limited.N == 0 {
		return errors.New("external response too large")
	}
	done, err := dispatch()
	if err != nil {
		return err
	}
	if done {
		if limited.N == 0 {
			return errors.New("external response too large")
		}
		return nil
	}
	return errors.New("stream termination missing")
}

func aiJSONEvent(data string, out any) error {
	if strings.TrimSpace(data) == "" {
		return errors.New("empty stream event")
	}
	if err := json.Unmarshal([]byte(data), out); err != nil {
		return errors.New("malformed stream event")
	}
	return nil
}

func aiStreamEmit(callback aiStreamCallback, kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return errors.New("stream event encoding failed")
	}
	if err := callback(kind, json.RawMessage(data)); err != nil {
		return err
	}
	return nil
}

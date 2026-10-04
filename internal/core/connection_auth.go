package core

import (
	"context"
	"strings"
	"time"
)

// Management mutations disconnect immediately. This shared sweep additionally
// catches changes made outside management without one SQL query/timer per socket.
func (c *Core) connectionAuthLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.Context.Done():
			return
		case <-ticker.C:
			c.validateConnections()
		}
	}
}

func (c *Core) validateConnections() {
	type deviceConnections struct {
		id          string
		connections []*connection
	}
	c.mu.Lock()
	devices := make([]deviceConnections, 0, len(c.connections))
	for id, set := range c.connections {
		item := deviceConnections{id: id, connections: make([]*connection, 0, len(set))}
		for conn := range set {
			item.connections = append(item.connections, conn)
		}
		devices = append(devices, item)
	}
	c.mu.Unlock()
	for start := 0; start < len(devices); start += 500 {
		batch := devices[start:min(start+500, len(devices))]
		args := make([]any, len(batch))
		marks := make([]string, len(batch))
		for i, device := range batch {
			args[i], marks[i] = device.id, "?"
		}
		ctx, cancel := context.WithTimeout(c.Context, 5*time.Second)
		active := make(map[string]string, len(batch))
		rows, err := c.reader().QueryContext(ctx, `SELECT d.id,d.user_id FROM devices d JOIN users u ON u.id=d.user_id WHERE d.revoked=0 AND u.disabled=0 AND d.id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err == nil {
			for rows.Next() {
				var device, user string
				if err = rows.Scan(&device, &user); err != nil {
					break
				}
				active[device] = user
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
		cancel()
		for _, device := range batch {
			for _, conn := range device.connections {
				if err != nil || active[device.id] != conn.id.UserID {
					conn.close()
				}
			}
		}
	}
}

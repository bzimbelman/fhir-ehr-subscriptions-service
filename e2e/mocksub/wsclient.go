// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mocksub

import (
	"context"
	"errors"
	"sync"

	"github.com/coder/websocket"
)

// WSClient is the subscriber-side WebSocket client. It dials a remote
// WS endpoint and journals every text frame it receives until close.
type WSClient struct {
	URL string

	mu      sync.Mutex
	frames  [][]byte
	conn    *websocket.Conn
	cancel  context.CancelFunc
	readerW sync.WaitGroup
}

// NewWSClient returns a client targeting url (`ws://...` or `wss://...`).
func NewWSClient(url string) *WSClient {
	return &WSClient{URL: url}
}

// Dial opens the connection and starts the read goroutine. Subsequent
// frames land in the journal until Close.
func (c *WSClient) Dial(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.URL, nil)
	if err != nil {
		return err
	}
	readerCtx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.conn = conn
	c.cancel = cancel
	c.mu.Unlock()
	c.readerW.Add(1)
	go c.readLoop(readerCtx)
	return nil
}

// Frames returns a copy of the journal.
func (c *WSClient) Frames() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.frames))
	for i, f := range c.frames {
		out[i] = append([]byte(nil), f...)
	}
	return out
}

// Close cancels reads and closes the underlying connection.
func (c *WSClient) Close() error {
	c.mu.Lock()
	conn := c.conn
	cancel := c.cancel
	c.conn = nil
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	c.readerW.Wait()
	return nil
}

func (c *WSClient) readLoop(ctx context.Context) {
	defer c.readerW.Done()
	for {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			return
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			return
		}
		c.mu.Lock()
		c.frames = append(c.frames, append([]byte(nil), data...))
		c.mu.Unlock()
	}
}

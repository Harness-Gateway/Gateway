package copilot

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/process"
)

const processWait = 2 * time.Second

// cliBridge owns the process and guards framing. The official SDK still owns
// JSON-RPC operations through its supported external-runtime connection.
type cliBridge struct {
	listener    net.Listener
	token       string
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	kill        func() error
	processDone <-chan struct{}
	workers     sync.WaitGroup
	once        sync.Once
	mu          sync.Mutex
	conn        net.Conn
	closed      bool
	failure     error
	cleanupErr  error
}

func startBridge(ctx context.Context, path, directory string) (*cliBridge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &cliBridge{listener: listener, token: hex.EncodeToString(secret[:])}
	cmd := exec.Command(path, "--headless", "--no-auto-update", "--log-level", "error", "--stdio")
	cmd.Dir = directory
	cmd.Env = os.Environ()
	cmd.WaitDelay = processWait
	stdin, input, err := os.Pipe()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	output, stdout, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = input.Close()
		_ = listener.Close()
		return nil, err
	}
	// Own the parent pipe ends explicitly: Cmd.Wait must not close stdout
	// before the bridge has drained a fast-exiting CLI's final frames.
	b.stdin, b.stdout = input, output
	cmd.Stdin, cmd.Stdout = stdin, stdout
	defer stdin.Close()
	defer stdout.Close()
	b.kill, err = process.Start(cmd)
	if err != nil {
		_ = b.stdin.Close()
		_ = b.stdout.Close()
		_ = listener.Close()
		return nil, err
	}
	done := make(chan struct{})
	b.processDone = done
	go func() {
		_ = cmd.Wait()
		// Also clean up children if the direct CLI exits first. Their inherited
		// pipe handles must not keep an otherwise dead connection open.
		_ = b.kill()
		close(done)
	}()
	b.workers.Add(1)
	go b.serve()
	return b, nil
}

func (b *cliBridge) serve() {
	defer b.workers.Done()
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			_ = conn.Close()
			return
		}
		b.conn = conn
		b.mu.Unlock()
		reader := bufio.NewReaderSize(conn, maxProtocolHeaderBytes)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		frame, err := authenticate(reader, b.token)
		if err != nil {
			_ = conn.Close()
			continue
		}
		_ = conn.SetReadDeadline(time.Time{})
		_ = b.listener.Close() // One authenticated SDK connection per operation.
		b.workers.Add(1)
		go func() {
			defer b.workers.Done()
			if err := copyFrames(conn, bufio.NewReaderSize(b.stdout, maxProtocolHeaderBytes)); err != nil {
				b.fail(err)
			}
		}()
		if _, err := fmt.Fprintf(b.stdin, "Content-Length: %d\r\n\r\n", len(frame)); err != nil {
			b.fail(err)
			return
		}
		if _, err := b.stdin.Write(frame); err != nil {
			b.fail(err)
			return
		}
		// Hide zero-copy interfaces: Linux splice caches pipe descriptors in a
		// runtime pool beyond this operation's lifetime. A fixed userspace buffer
		// keeps ownership and descriptor cleanup deterministic.
		if _, err := io.CopyBuffer(struct{ io.Writer }{b.stdin}, struct{ io.Reader }{reader}, make([]byte, 32*1024)); err != nil {
			b.fail(err)
		}
		return
	}
}

func authenticate(reader *bufio.Reader, token string) ([]byte, error) {
	length, err := frameLength(reader, maxConnectBytes)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(reader, frame); err != nil {
		return nil, err
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(frame, &request); err != nil {
		return nil, err
	}
	var method, provided string
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request["method"], &method); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(request["params"], &params); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(params["token"], &provided); err != nil {
		return nil, err
	}
	if method != "connect" || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
		return nil, errors.New("unauthorized copilot bridge connection")
	}
	// This token authenticates the SDK to our bridge, not the stdio runtime.
	// Older installed CLIs reject a token when their own transport auth is not
	// configured. Keep CLI authentication identical to the SDK's stdio mode.
	delete(params, "token")
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	request["params"] = encoded
	return json.Marshal(request)
}

func (b *cliBridge) fail(err error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if b.failure == nil {
		b.failure = fmt.Errorf("copilot transport: %w", err)
	}
	b.mu.Unlock()
	// Close waits for the workers, so it must run outside either worker.
	go func() { _ = b.Close() }()
}

func (b *cliBridge) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failure
}

func (b *cliBridge) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		conn := b.conn
		b.mu.Unlock()
		_ = b.listener.Close()
		if conn != nil {
			_ = conn.Close()
		}
		b.cleanupErr = b.kill()
		_ = b.stdin.Close()
		_ = b.stdout.Close()
		select {
		case <-b.processDone:
		case <-time.After(processWait):
			b.cleanupErr = errors.Join(b.cleanupErr, errors.New("timed out waiting for copilot process to exit"))
		}
		b.workers.Wait()
	})
	return b.cleanupErr
}

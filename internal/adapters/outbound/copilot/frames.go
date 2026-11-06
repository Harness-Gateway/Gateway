package copilot

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/harness-gateway/harness-gateway/internal/domain"
)

// Bound wire bytes before the SDK allocates its Content-Length buffer. JSON
// escaping can expand a decoded UTF-8 byte to six bytes; allow envelope overhead.
const maxProtocolFrameBytes = 6*domain.MaxResponseBytes + 1024*1024
const maxProtocolHeaderBytes = 8 * 1024
const maxConnectBytes = 64 * 1024

func frameLength(reader *bufio.Reader, limit int) (int, error) {
	size, length := 0, -1
	for {
		line, err := reader.ReadSlice('\n')
		size += len(line)
		if size > maxProtocolHeaderBytes || errors.Is(err, bufio.ErrBufferFull) {
			return 0, errors.New("copilot protocol header exceeds 8 KiB")
		}
		if err != nil {
			return 0, err
		}
		lineText := strings.TrimSpace(string(line))
		if lineText == "" {
			break
		}
		name, value, ok := strings.Cut(lineText, ":")
		if !ok {
			return 0, errors.New("invalid copilot protocol header")
		}
		if !strings.EqualFold(name, "Content-Length") {
			continue
		}
		if length != -1 {
			return 0, errors.New("duplicate copilot Content-Length")
		}
		value = strings.TrimSpace(value)
		// Reject signs and other representations before integer conversion.
		if value == "" || strings.IndexFunc(value, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
			return 0, errors.New("invalid copilot Content-Length")
		}
		length, err = strconv.Atoi(value)
		if err != nil || length <= 0 || length > limit {
			return 0, fmt.Errorf("copilot protocol frame exceeds limit or has invalid length (maximum %d bytes)", limit)
		}
	}
	if length < 0 {
		return 0, errors.New("missing copilot Content-Length")
	}
	return length, nil
}

func copyFrames(dst io.Writer, src *bufio.Reader) error {
	buffer := make([]byte, 32*1024)
	for {
		length, err := frameLength(src, maxProtocolFrameBytes)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(dst, "Content-Length: %d\r\n\r\n", length); err != nil {
			return err
		}
		n, err := io.CopyBuffer(struct{ io.Writer }{dst}, io.LimitReader(struct{ io.Reader }{src}, int64(length)), buffer)
		if err != nil {
			return err
		}
		if n != int64(length) {
			return io.ErrUnexpectedEOF
		}
	}
}

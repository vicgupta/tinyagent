package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxMessageBytes caps one message from a server.
const maxMessageBytes = 64 << 20

// message is any JSON-RPC 2.0 message: request, notification or response.
type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// request is an outgoing request or notification (ID nil).
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// reply answers a server-to-client request.
type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
}

// writeMessage frames v with a Content-Length header.
func writeMessage(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf := make([]byte, 0, len(body)+32)
	buf = append(buf, "Content-Length: "...)
	buf = strconv.AppendInt(buf, int64(len(body)), 10)
	buf = append(buf, "\r\n\r\n"...)
	buf = append(buf, body...)
	_, err = w.Write(buf)
	return err
}

// readMessage reads one framed message body.
func readMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, errors.New("lsp: header line too long")
			}
			return nil, err
		}
		s := strings.TrimRight(string(line), "\r\n")
		if s == "" {
			if length < 0 {
				return nil, errors.New("lsp: message without Content-Length")
			}
			break
		}
		k, v, ok := strings.Cut(s, ":")
		if !ok {
			return nil, fmt.Errorf("lsp: malformed header %q", s)
		}
		if strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("lsp: bad Content-Length %q", v)
			}
			length = n
		}
	}
	if length > maxMessageBytes {
		return nil, fmt.Errorf("lsp: message of %d bytes exceeds the %d byte limit", length, maxMessageBytes)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

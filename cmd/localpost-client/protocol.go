package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"uuid"
)

const storedLimit = 13981020

type streamID struct {
	milliseconds uint64
	sequence     uint64
}

func parseStreamID(text string) (streamID, bool) {
	parts := strings.Split(text, "-")
	if len(parts) != 2 {
		return streamID{}, false
	}
	var values [2]uint64
	for i, part := range parts {
		if part == "" {
			return streamID{}, false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return streamID{}, false
			}
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return streamID{}, false
		}
		values[i] = value
	}
	return streamID{milliseconds: values[0], sequence: values[1]}, true
}
func (id streamID) greater(other streamID) bool {
	return id.milliseconds > other.milliseconds || id.milliseconds == other.milliseconds && id.sequence > other.sequence
}

// object rejects duplicate keys as well as extra keys, including escaped aliases.
func object(data []byte, expected ...string) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	result := make(map[string]json.RawMessage, len(expected))
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, false
		}
		key, ok := token.(string)
		if !ok {
			return nil, false
		}
		if _, exists := result[key]; exists {
			return nil, false
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, false
		}
		result[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	if len(result) != len(expected) {
		return nil, false
	}
	for _, key := range expected {
		if _, ok := result[key]; !ok {
			return nil, false
		}
	}
	return result, true
}

func commandResponse(client *http.Client, cfg config, command string, args []string, payload *string, limit int64) (json.RawMessage, *failure) {
	components := append([]string{command}, args...)
	for i := range components {
		components[i] = strings.ReplaceAll(url.QueryEscape(components[i]), "+", "%20")
	}
	encoded := strings.Join(components, "/")
	method, target, body := http.MethodPost, strings.TrimSuffix(cfg.endpoint, "/")+"/", encoded
	if payload != nil {
		method, target, body = http.MethodPut, target+encoded, *payload
	}
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		return nil, fail("invalid_argument", 2)
	}
	request.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("Pragma", "no-cache")
	response, err := client.Do(request)
	if err != nil {
		return nil, fail("transport_error", 1)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fail("http_error", 1)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fail("transport_error", 1)
	}
	if int64(len(data)) > limit {
		return nil, fail("response_too_large", 1)
	}
	envelope, ok := object(data, command)
	if !ok {
		return nil, fail("invalid_response", 1)
	}
	raw := envelope[command]
	var backend []json.RawMessage
	if json.Unmarshal(raw, &backend) == nil && len(backend) == 2 && string(backend[0]) == "false" {
		var message string
		if json.Unmarshal(backend[1], &message) == nil {
			if strings.HasPrefix(message, "OOM") {
				return nil, fail("memory_limit", 1)
			}
			return nil, fail("backend_error", 1)
		}
	}
	return raw, nil
}

type message struct {
	ID    string `json:"id"`
	Bytes int    `json:"bytes"`
	Data  string `json:"data"`
}

func readMessages(raw json.RawMessage, cfg config) ([]message, *failure) {
	var entries []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &entries) != nil || len(entries) > cfg.count {
		return nil, fail("invalid_response", 1)
	}
	messages := make([]message, 0, len(entries))
	previous, havePrevious := parseStreamID(cfg.after)
	for _, entry := range entries {
		fields, ok := object(entry, "id", "msg")
		if !ok {
			return nil, fail("invalid_response", 1)
		}
		var id string
		if json.Unmarshal(fields["id"], &id) != nil {
			return nil, fail("invalid_response", 1)
		}
		parsed, valid := parseStreamID(id)
		if !valid || havePrevious && !parsed.greater(previous) || cfg.operation == "get" && id != cfg.messageID {
			return nil, fail("invalid_response", 1)
		}
		content, ok := object(fields["msg"], "data")
		if !ok {
			return nil, fail("invalid_response", 1)
		}
		var stored string
		if json.Unmarshal(content["data"], &stored) != nil || !strings.HasPrefix(stored, "b64:") || len(stored) > storedLimit {
			return nil, fail("invalid_response", 1)
		}
		encoded := strings.TrimPrefix(stored, "b64:")
		decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil || len(decoded) > maxMessage || base64.StdEncoding.EncodeToString(decoded) != encoded {
			return nil, fail("invalid_response", 1)
		}
		messages = append(messages, message{ID: id, Bytes: len(decoded), Data: encoded})
		previous, havePrevious = parsed, true
	}
	return messages, nil
}

func perform(client *http.Client, cfg config, input io.Reader) ([]byte, *failure) {
	key := "localpost:v1:" + cfg.mailbox
	command := ""
	var args []string
	var payload *string
	limit := int64(64 * 1024)
	size := 0
	switch cfg.operation {
	case "register":
		command = "PING"
	case "send":
		data, err := io.ReadAll(io.LimitReader(input, maxMessage+1))
		if err != nil {
			return nil, fail("input_error", 2)
		}
		if len(data) > maxMessage {
			return nil, fail("input_too_large", 2)
		}
		size = len(data)
		encoded := "b64:" + base64.StdEncoding.EncodeToString(data)
		payload = &encoded
		command, args = "XADD", []string{key, "*", "data"}
	case "read", "get":
		start, end := "-", "+"
		if cfg.after != "" {
			start = "(" + cfg.after
		}
		if cfg.operation == "get" {
			start, end = cfg.messageID, cfg.messageID
		}
		command, args = "XRANGE", []string{key, start, end, "COUNT", strconv.Itoa(cfg.count)}
		limit = int64(cfg.count)*(storedLimit+256) + 256
	case "clear":
		command, args = "XTRIM", []string{key, "MAXLEN", "=", "0"}
	case "delete":
		command, args = "DEL", []string{key}
	}
	raw, problem := commandResponse(client, cfg, command, args, payload, limit)
	if problem != nil {
		return nil, problem
	}
	switch cfg.operation {
	case "register":
		var pong []json.RawMessage
		if json.Unmarshal(raw, &pong) != nil || len(pong) != 2 || string(pong[0]) != "true" {
			return nil, fail("invalid_response", 1)
		}
		var pongText string
		if json.Unmarshal(pong[1], &pongText) != nil || pongText != "PONG" {
			return nil, fail("invalid_response", 1)
		}
		id := uuid.NewV4().String()
		return jsonOutput(struct {
			ID string `json:"id"`
		}{ID: id})
	case "send":
		var id string
		if json.Unmarshal(raw, &id) != nil {
			return nil, fail("invalid_response", 1)
		}
		if _, ok := parseStreamID(id); !ok {
			return nil, fail("invalid_response", 1)
		}
		return jsonOutput(struct {
			ID        string `json:"id"`
			MessageID string `json:"message_id"`
			Bytes     int    `json:"bytes"`
		}{cfg.mailbox, id, size})
	case "read", "get":
		messages, problem := readMessages(raw, cfg)
		if problem != nil {
			return nil, problem
		}
		if cfg.operation == "get" {
			if len(messages) == 0 {
				return nil, fail("message_not_found", 3)
			}
			// readMessages has already checked the canonical encoding for this entry.
			data, err := base64.StdEncoding.DecodeString(messages[0].Data)
			if err != nil {
				return nil, fail("invalid_response", 1)
			}
			return data, nil
		}
		var next *string
		if len(messages) > 0 {
			next = &messages[len(messages)-1].ID
		}
		return jsonOutput(struct {
			ID       string    `json:"id"`
			Messages []message `json:"messages"`
			Next     *string   `json:"next_after"`
		}{cfg.mailbox, messages, next})
	case "clear", "delete":
		// Decoding into uint64 rejects negative, fractional, null, and overflowing counts.
		var count uint64
		if string(raw) == "null" || json.Unmarshal(raw, &count) != nil || cfg.operation == "delete" && count > 1 {
			return nil, fail("invalid_response", 1)
		}
		if cfg.operation == "clear" {
			return jsonOutput(struct {
				ID      string `json:"id"`
				Cleared uint64 `json:"cleared"`
			}{cfg.mailbox, count})
		}
		return jsonOutput(struct {
			ID      string `json:"id"`
			Deleted bool   `json:"deleted"`
		}{cfg.mailbox, count == 1})
	}
	return nil, fail("invalid_argument", 2)
}

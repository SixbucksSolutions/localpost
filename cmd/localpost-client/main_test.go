package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testMailbox = "123e4567-e89b-42d3-a456-426614174000"

func invoke(t *testing.T, response string, args []string, input []byte) (int, string, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
	defer server.Close()
	return invokeURL(server.URL, args, bytes.NewReader(input))
}
func invokeURL(endpoint string, args []string, input io.Reader) (int, string, string) {
	var stdout, stderr bytes.Buffer
	exit := run(append([]string{"--url", endpoint}, args...), input, &stdout, &stderr)
	return exit, stdout.String(), stderr.String()
}
func requireError(t *testing.T, exit int, stdout, stderr, operation, code string, expectedExit int) {
	t.Helper()
	expected := fmt.Sprintf("{\"error\":{\"code\":%q,\"operation\":%q}}\n", code, operation)
	if exit != expectedExit || stdout != "" || stderr != expected {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want %d %q", exit, stdout, stderr, expectedExit, expected)
	}
}
func entry(id, data string) string { return fmt.Sprintf(`{"id":%q,"msg":{"data":%q}}`, id, data) }

func TestCommands(t *testing.T) {
	tests := []struct {
		name, response, expected string
		args                     []string
		input                    []byte
	}{
		{"send binary", `{"XADD":"10-2"}`, fmt.Sprintf("{\"id\":%q,\"message_id\":\"10-2\",\"bytes\":4}\n", testMailbox), []string{"send", testMailbox}, []byte{0, 255, 13, 10}},
		{"send empty", `{"XADD":"10-2"}`, fmt.Sprintf("{\"id\":%q,\"message_id\":\"10-2\",\"bytes\":0}\n", testMailbox), []string{"send", testMailbox}, nil},
		{"get binary", `{"XRANGE":[` + entry("10-2", "b64:AP8NCg==") + `]}`, string([]byte{0, 255, 13, 10}), []string{"get", testMailbox, "10-2"}, nil},
		{"get empty", `{"XRANGE":[` + entry("10-2", "b64:") + `]}`, "", []string{"get", testMailbox, "10-2"}, nil},
		{"read empty", `{"XRANGE":[]}`, fmt.Sprintf("{\"id\":%q,\"messages\":[],\"next_after\":null}\n", testMailbox), []string{"read", testMailbox}, nil},
		{"read", `{"XRANGE":[` + entry("10-2", "b64:AP8NCg==") + `,` + entry("10-3", "b64:") + `]}`, fmt.Sprintf("{\"id\":%q,\"messages\":[{\"id\":\"10-2\",\"bytes\":4,\"data\":\"AP8NCg==\"},{\"id\":\"10-3\",\"bytes\":0,\"data\":\"\"}],\"next_after\":\"10-3\"}\n", testMailbox), []string{"read", testMailbox, "--count", "2", "--after", "10-1"}, nil},
		{"clear", `{"XTRIM":42}`, fmt.Sprintf("{\"id\":%q,\"cleared\":42}\n", testMailbox), []string{"clear", testMailbox}, nil},
		{"delete", `{"DEL":1}`, fmt.Sprintf("{\"id\":%q,\"deleted\":true}\n", testMailbox), []string{"delete", testMailbox}, nil},
		{"delete absent", `{"DEL":0}`, fmt.Sprintf("{\"id\":%q,\"deleted\":false}\n", testMailbox), []string{"delete", testMailbox}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exit, out, errOut := invoke(t, tt.response, tt.args, tt.input)
			if exit != 0 || out != tt.expected || errOut != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out, errOut)
			}
		})
	}
}

func TestWireCommands(t *testing.T) {
	tests := []struct {
		args                         []string
		method, path, body, response string
		input                        []byte
	}{
		{[]string{"register"}, "POST", "/", "PING", `{"PING":[true,"PONG"]}`, nil},
		{[]string{"send", testMailbox}, "PUT", "/XADD/localpost:v1:" + testMailbox + "/*/data", "b64:", `{"XADD":"1-0"}`, nil},
		{[]string{"send", testMailbox}, "PUT", "/XADD/localpost:v1:" + testMailbox + "/*/data", "b64:AP8NCg==", `{"XADD":"1-0"}`, []byte{0, 255, 13, 10}},
		{[]string{"read", testMailbox, "--after", "1-2", "--count", "16"}, "POST", "/", "XRANGE/localpost%3Av1%3A" + testMailbox + "/%281-2/%2B/COUNT/16", `{"XRANGE":[]}`, nil},
		{[]string{"get", testMailbox, "1-2"}, "POST", "/", "XRANGE/localpost%3Av1%3A" + testMailbox + "/1-2/1-2/COUNT/1", `{"XRANGE":[` + entry("1-2", "b64:") + `]}`, nil},
		{[]string{"clear", testMailbox}, "POST", "/", "XTRIM/localpost%3Av1%3A" + testMailbox + "/MAXLEN/%3D/0", `{"XTRIM":0}`, nil},
		{[]string{"delete", testMailbox}, "POST", "/", "DEL/localpost%3Av1%3A" + testMailbox, `{"DEL":0}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.args[0]+tt.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if r.Method != tt.method || r.URL.Path != tt.path || string(body) != tt.body {
					t.Errorf("wire: %s %s %q", r.Method, r.URL.Path, body)
				}
				if r.Header.Get("Content-Type") != "text/plain;charset=UTF-8" || r.Header.Get("Cache-Control") != "no-store" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Errorf("headers: %v", r.Header)
				}
				_, _ = io.WriteString(w, tt.response)
			}))
			defer server.Close()
			exit, out, errOut := invokeURL(server.URL, tt.args, bytes.NewReader(tt.input))
			if exit != 0 {
				t.Fatalf("exit=%d %s", exit, errOut)
			}
			if tt.args[0] == "register" {
				var result struct {
					ID string `json:"id"`
				}
				if json.Unmarshal([]byte(out), &result) != nil || !mailboxPattern.MatchString(result.ID) {
					t.Fatalf("invalid generated ID %q", out)
				}
			}
		})
	}
}

func TestInvalidResponses(t *testing.T) {
	tests := []struct {
		operation, response string
		tail                []string
	}{
		{"register", `{"PING":[true,"pong"]}`, nil}, {"register", `{"PING":[1,"PONG"]}`, nil}, {"register", `{"PING":[true,"PONG",0]}`, nil},
		{"send", `{"XADD":123}`, nil}, {"send", `{"XADD":"18446744073709551616-0"}`, nil}, {"send", `{"XADD":"-1-0"}`, nil},
		{"send", `{"XADD":"1-0","other":0}`, nil}, {"send", `{"XADD":"1-0","XADD":"2-0"}`, nil},
		{"send", `{"XADD":"1-0","\u0058ADD":"2-0"}`, nil}, {"send", `{"XADD":"1-0"} {}`, nil},
		{"send", `null`, nil}, {"send", `[]`, nil}, {"send", `{"XADD":[false,3]}`, nil},
		{"clear", `{"XTRIM":null}`, nil}, {"clear", `{"XTRIM":-1}`, nil}, {"clear", `{"XTRIM":1.0}`, nil}, {"clear", `{"XTRIM":"1"}`, nil},
		{"clear", `{"XTRIM":18446744073709551616}`, nil}, {"delete", `{"DEL":2}`, nil}, {"delete", `{"DEL":false}`, nil},
		{"read", `{"XRANGE":null}`, nil}, {"read", `{"XRANGE":{}}`, nil}, {"read", `{"XRANGE":[null]}`, nil},
		{"read", `{"XRANGE":[{"id":"1-0","msg":{"data":"b64:"},"other":1}]}`, nil},
		{"read", `{"XRANGE":[{"id":"1-0","id":"1-1","msg":{"data":"b64:"}}]}`, nil},
		{"read", `{"XRANGE":[{"id":"1-0","msg":{"data":"b64:","other":1}}]}`, nil},
		{"read", `{"XRANGE":[{"id":"1-0","msg":{"data":"b64:","data":"b64:"}}]}`, nil},
		{"read", `{"XRANGE":[` + entry("1-0", "") + `]}`, nil}, {"read", `{"XRANGE":[` + entry("1-0", "b64:AA") + `]}`, nil},
		{"read", `{"XRANGE":[` + entry("1-0", "b64:AB==") + `]}`, nil}, {"read", `{"XRANGE":[` + entry("1-0", "b64:AA==\n") + `]}`, nil},
		{"read", `{"XRANGE":[` + entry("1-0", "b64:AA==\r") + `]}`, nil}, {"read", `{"XRANGE":[` + entry("1-0", "b64:_w==") + `]}`, nil},
		{"read", `{"XRANGE":[` + entry("1-0", "b64:") + `,` + entry("1-1", "b64:") + `]}`, nil},
		{"read", `{"XRANGE":[` + entry("2-0", "b64:") + `,` + entry("1-0", "b64:") + `]}`, []string{"--count", "2"}},
		{"read", `{"XRANGE":[` + entry("1-0", "b64:") + `,` + entry("1-0", "b64:") + `]}`, []string{"--count", "2"}},
		{"read", `{"XRANGE":[` + entry("1-0", "b64:") + `]}`, []string{"--after", "1-0"}},
		{"read", `{"XRANGE":[` + entry("1-0", "b64:") + `]}`, []string{"--after", "2-0"}},
		{"get", `{"XRANGE":[` + entry("1-1", "b64:") + `]}`, []string{"1-0"}},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			args := []string{tt.operation}
			if tt.operation != "register" {
				args = append(args, testMailbox)
			}
			args = append(args, tt.tail...)
			exit, out, errOut := invoke(t, tt.response, args, nil)
			requireError(t, exit, out, errOut, tt.operation, "invalid_response", 1)
		})
	}
}

func TestBackendErrors(t *testing.T) {
	for _, command := range []struct{ operation, key string }{{"register", "PING"}, {"send", "XADD"}, {"read", "XRANGE"}, {"get", "XRANGE"}, {"clear", "XTRIM"}, {"delete", "DEL"}} {
		for _, backend := range []struct{ text, code string }{{"OOM private backend details", "memory_limit"}, {"ERR private backend details", "backend_error"}} {
			t.Run(command.operation+backend.code, func(t *testing.T) {
				args := []string{command.operation}
				if command.operation != "register" {
					args = append(args, testMailbox)
				}
				if command.operation == "get" {
					args = append(args, "1-0")
				}
				exit, out, errOut := invoke(t, fmt.Sprintf(`{%q:[false,%q]}`, command.key, backend.text), args, nil)
				requireError(t, exit, out, errOut, command.operation, backend.code, 1)
			})
		}
	}
	exit, out, errOut := invoke(t, `{"XRANGE":[]}`, []string{"get", testMailbox, "1-0"}, nil)
	requireError(t, exit, out, errOut, "get", "message_not_found", 3)
}

func TestArguments(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1", "http://localhost", "http://127.0.0.2", "http://[::2]", "http://user@127.0.0.1", "http://127.0.0.1?", "http://127.0.0.1#", "http://127.0.0.1/%2f", "http://127.0.0.1/a", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:", "http://127.0.0.1:+80"} {
		t.Run(endpoint, func(t *testing.T) {
			exit, out, errOut := invokeURL(endpoint, []string{"register"}, strings.NewReader(""))
			requireError(t, exit, out, errOut, "register", "invalid_argument", 2)
		})
	}
	for _, endpoint := range []string{"http://127.0.0.1", "http://127.0.0.1/", "http://[::1]", "http://[::1]:65535/", "http://127.0.0.1:00080"} {
		if !validEndpoint(endpoint) {
			t.Errorf("valid endpoint rejected: %s", endpoint)
		}
	}
	invalid := [][]string{{}, {"unknown"}, {"register", "extra"}, {"send"}, {"send", strings.ToUpper(testMailbox)}, {"send", strings.Replace(testMailbox, "42d3", "12d3", 1)}, {"send", testMailbox, "extra"}, {"get", testMailbox, "+1-0"}, {"get", testMailbox, "1-18446744073709551616"}, {"read", testMailbox, "--count", "0"}, {"read", testMailbox, "--count", "17"}, {"read", testMailbox, "--after", ""}, {"read", testMailbox, "--after", "bad"}, {"read", testMailbox, "extra"}, {"--timeout", "0s", "register"}, {"--timeout", "-1s", "register"}, {"--bad"}, {"--version", "send", testMailbox}}
	for _, args := range invalid {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			cfg, problem := parse(args)
			if problem == nil || problem.code != "invalid_argument" || problem.exit != 2 {
				t.Fatalf("accepted invalid args %v: %+v", args, cfg)
			}
		})
	}
	for _, args := range [][]string{{"--version"}, {"--help"}, {"help"}, {"read", "--help"}} {
		var out, errOut bytes.Buffer
		if exit := run(args, strings.NewReader(""), &out, &errOut); exit != 0 || out.Len() == 0 || errOut.Len() != 0 {
			t.Fatalf("help/version failed: %v", args)
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("private input error") }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("private output error") }
func TestIOFailures(t *testing.T) {
	exit, out, errOut := invokeURL("http://127.0.0.1:1", []string{"send", testMailbox}, failingReader{})
	requireError(t, exit, out, errOut, "send", "input_error", 2)
	exit, out, errOut = invokeURL("http://127.0.0.1:1", []string{"send", testMailbox}, io.LimitReader(strings.NewReader(strings.Repeat("x", maxMessage+1)), maxMessage+1))
	requireError(t, exit, out, errOut, "send", "input_too_large", 2)
	var stderr bytes.Buffer
	if exit := run([]string{"--version"}, strings.NewReader(""), failingWriter{}, &stderr); exit != 1 || !strings.Contains(stderr.String(), `"code":"output_error"`) {
		t.Fatalf("output failure: %d %s", exit, stderr.String())
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "http://127.0.0.1:1", http.StatusTemporaryRedirect)
	}))
	exit, out, errOut = invokeURL(server.URL, []string{"register"}, strings.NewReader(""))
	requireError(t, exit, out, errOut, "register", "http_error", 1)
	server.Close()
	exit, out, errOut = invokeURL(server.URL, []string{"register"}, strings.NewReader(""))
	requireError(t, exit, out, errOut, "register", "transport_error", 1)
	exit, out, errOut = invoke(t, strings.Repeat(" ", 64*1024+1), []string{"register"}, nil)
	requireError(t, exit, out, errOut, "register", "response_too_large", 1)
}

func TestNumericStreamOrdering(t *testing.T) {
	response := `{"XRANGE":[` + entry("9-18446744073709551615", "b64:") + `,` + entry("10-0", "b64:") + `,` + entry("18446744073709551615-18446744073709551615", "b64:") + `]}`
	exit, out, errOut := invoke(t, response, []string{"read", testMailbox, "--count", "3", "--after", "9-2"}, nil)
	if exit != 0 {
		t.Fatalf("numeric ordering: %s %s", out, errOut)
	}
}

func TestMessageSizeBoundaries(t *testing.T) {
	payload := bytes.Repeat([]byte{255}, maxMessage)
	encoded := "b64:" + base64.StdEncoding.EncodeToString(payload)
	if len(encoded) != storedLimit {
		t.Fatal("incorrect boundary fixture")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if !bytes.Equal(body, []byte(encoded)) {
			t.Errorf("maximum payload was not preserved")
		}
		_, _ = io.WriteString(w, `{"XADD":"1-0"}`)
	}))
	defer server.Close()
	exit, out, errOut := invokeURL(server.URL, []string{"send", testMailbox}, bytes.NewReader(payload))
	if exit != 0 || !strings.Contains(out, `"bytes":10485760`) {
		t.Fatalf("maximum send: %d %s %s", exit, out, errOut)
	}
	exit, out, errOut = invoke(t, `{"XRANGE":[`+entry("1-0", encoded)+`]}`, []string{"get", testMailbox, "1-0"}, nil)
	if exit != 0 || !bytes.Equal([]byte(out), payload) {
		t.Fatalf("maximum get: %d %s", exit, errOut)
	}
	tooBig := "b64:" + base64.StdEncoding.EncodeToString(append(payload, 0))
	exit, out, errOut = invoke(t, `{"XRANGE":[`+entry("1-0", tooBig)+`]}`, []string{"read", testMailbox}, nil)
	requireError(t, exit, out, errOut, "read", "invalid_response", 1)
	exit, out, errOut = invoke(t, strings.Repeat(" ", storedLimit+256+256+1), []string{"read", testMailbox}, nil)
	requireError(t, exit, out, errOut, "read", "response_too_large", 1)
}

func TestEscapedPongAndHelp(t *testing.T) {
	exit, out, errOut := invoke(t, `{"PING":[true,"\u0050ONG"]}`, []string{"register"}, nil)
	if exit != 0 || out == "" || errOut != "" {
		t.Fatalf("escaped pong: %d %s", exit, errOut)
	}
	var stdout, stderr bytes.Buffer
	exit = run([]string{"read", testMailbox, "--help"}, strings.NewReader(""), &stdout, &stderr)
	if exit != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
		t.Fatalf("read help: %d %s", exit, stderr.String())
	}
}

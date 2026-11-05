// Package main provides the stateless localpost Webdis client.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var releaseVersion = "dev"
var sourceCommit = "unknown"

const maxMessage = 10 * 1024 * 1024
const usage = `localpost-client [--url http://127.0.0.1:53147] [--timeout 30s] command
Commands:
  register
  send <mailbox-id>
  read <mailbox-id> [--after <stream-id>] [--count <1..16>]
  get <mailbox-id> <stream-id>
  clear <mailbox-id>
  delete <mailbox-id>
Use --version to print build identity.
`

var endpointPattern = regexp.MustCompile(`^http://(127\.0\.0\.1|\[::1\])(:[0-9]+)?/?$`)
var mailboxPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type config struct {
	endpoint  string
	timeout   time.Duration
	operation string
	mailbox   string
	messageID string
	after     string
	count     int
	version   bool
	help      bool
}

type failure struct {
	code string
	exit int
}

func fail(code string, exit int) *failure { return &failure{code: code, exit: exit} }

func parse(args []string) (config, *failure) {
	cfg := config{count: 1}
	flags := flag.NewFlagSet("localpost-client", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.endpoint, "url", "http://127.0.0.1:53147", "Webdis URL")
	flags.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "HTTP timeout")
	flags.BoolVar(&cfg.version, "version", false, "Print version")
	flags.BoolVar(&cfg.help, "help", false, "Print help")
	flags.BoolVar(&cfg.help, "h", false, "Print help")
	if err := flags.Parse(args); err != nil {
		return cfg, fail("invalid_argument", 2)
	}
	rest := flags.Args()
	if cfg.help {
		return cfg, nil
	}
	if cfg.version && len(rest) == 0 {
		return cfg, nil
	}
	if len(rest) == 0 {
		return cfg, fail("invalid_argument", 2)
	}
	cfg.operation = rest[0]
	if cfg.operation == "help" && len(rest) == 1 {
		cfg.help = true
		return cfg, nil
	}
	if cfg.version || !validEndpoint(cfg.endpoint) || cfg.timeout <= 0 {
		return cfg, fail("invalid_argument", 2)
	}
	rest = rest[1:]
	if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
		cfg.help = true
		return cfg, nil
	}
	switch cfg.operation {
	case "register":
		if len(rest) != 0 {
			return cfg, fail("invalid_argument", 2)
		}
	case "send", "clear", "delete":
		if len(rest) != 1 {
			return cfg, fail("invalid_argument", 2)
		}
		cfg.mailbox = rest[0]
	case "get":
		if len(rest) != 2 {
			return cfg, fail("invalid_argument", 2)
		}
		cfg.mailbox, cfg.messageID = rest[0], rest[1]
		if _, ok := parseStreamID(cfg.messageID); !ok {
			return cfg, fail("invalid_argument", 2)
		}
	case "read":
		if len(rest) == 0 {
			return cfg, fail("invalid_argument", 2)
		}
		cfg.mailbox = rest[0]
		readFlags := flag.NewFlagSet("read", flag.ContinueOnError)
		readFlags.SetOutput(io.Discard)
		readFlags.StringVar(&cfg.after, "after", "", "Exclusive lower ID")
		readFlags.IntVar(&cfg.count, "count", 1, "Page size")
		if err := readFlags.Parse(rest[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				cfg.help = true
				return cfg, nil
			}
			return cfg, fail("invalid_argument", 2)
		}
		if readFlags.NArg() != 0 || cfg.count < 1 || cfg.count > 16 {
			return cfg, fail("invalid_argument", 2)
		}
		suppliedAfter := false
		readFlags.Visit(func(f *flag.Flag) {
			if f.Name == "after" {
				suppliedAfter = true
			}
		})
		if suppliedAfter {
			if _, ok := parseStreamID(cfg.after); !ok {
				return cfg, fail("invalid_argument", 2)
			}
		}
	default:
		return cfg, fail("invalid_argument", 2)
	}
	if cfg.operation != "register" && !mailboxPattern.MatchString(cfg.mailbox) {
		return cfg, fail("invalid_argument", 2)
	}
	return cfg, nil
}

func validEndpoint(endpoint string) bool {
	if !endpointPattern.MatchString(endpoint) {
		return false
	}
	authority := strings.TrimSuffix(strings.TrimPrefix(endpoint, "http://"), "/")
	if strings.HasPrefix(authority, "[::1]") {
		authority = strings.TrimPrefix(authority, "[::1]")
	} else {
		authority = strings.TrimPrefix(authority, "127.0.0.1")
	}
	if authority == "" {
		return true
	}
	port, err := strconv.ParseUint(strings.TrimPrefix(authority, ":"), 10, 16)
	return err == nil && port > 0
}

func execute(cfg config, input io.Reader) ([]byte, *failure) {
	if cfg.help {
		return []byte(usage), nil
	}
	if cfg.version {
		return []byte(fmt.Sprintf("localpost-client %s (commit %s; %s; %s/%s)\n", releaseVersion, sourceCommit, runtime.Version(), runtime.GOOS, runtime.GOARCH)), nil
	}
	transport := &http.Transport{DisableKeepAlives: true, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: cfg.timeout, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return perform(client, cfg, input)
}

func run(args []string, input io.Reader, output, errorOutput io.Writer) int {
	cfg, problem := parse(args)
	var result []byte
	if problem == nil {
		result, problem = execute(cfg, input)
	}
	if problem == nil {
		n, err := output.Write(result)
		if err == nil && n == len(result) {
			return 0
		}
		problem = fail("output_error", 1)
	}
	diagnostic := struct {
		Error struct {
			Code      string `json:"code"`
			Operation string `json:"operation"`
		} `json:"error"`
	}{}
	diagnostic.Error.Code, diagnostic.Error.Operation = problem.code, cfg.operation
	// There is no further output channel if stderr itself fails.
	_ = json.NewEncoder(errorOutput).Encode(diagnostic)
	return problem.exit
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func jsonOutput(value any) ([]byte, *failure) {
	var output bytes.Buffer
	if err := json.NewEncoder(&output).Encode(value); err != nil {
		return nil, fail("output_error", 1)
	}
	return output.Bytes(), nil
}

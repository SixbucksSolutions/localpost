// Command release builds reproducible, unqualified localpost candidate archives.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const toolchain = "go1.27.3"

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var archiveTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

type entry struct {
	name string
	data []byte
	mode int64
}
type commandRunner func(string, []string, string, ...string) ([]byte, error)
type manifest struct {
	Version      string            `json:"version"`
	SourceCommit string            `json:"sourceCommit"`
	Toolchain    string            `json:"toolchain"`
	Target       string            `json:"target"`
	Settings     map[string]string `json:"settings"`
	BinarySHA256 string            `json:"binarySHA256"`
}

func main() {
	version := flag.String("version", "", "release version (for example 0.1.0)")
	output := flag.String("output", "", "new or empty candidate output directory")
	sourceRef := flag.String("source-ref", "HEAD", "Git source revision to archive")
	targets := flag.String("targets", "windows/amd64,linux/amd64", "comma-separated package targets")
	flag.Parse()
	if flag.NArg() != 0 {
		// Reporting failure is best effort immediately before process exit.
		_, _ = fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(1)
	}
	if err := release(*version, *output, *sourceRef, *targets, runCommand); err != nil {
		// Reporting failure is best effort immediately before process exit.
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCommand(dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	result, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return result, nil
}

func git(run commandRunner, dir string, args ...string) ([]byte, error) {
	// Scope the trust exception to this explicitly selected checkout only.
	prefix := []string{"-c", "safe.directory=" + filepath.ToSlash(dir)}
	return run(dir, os.Environ(), "git", append(prefix, args...)...)
}

func preflight(run commandRunner, dir, version string) (string, error) {
	if !versionPattern.MatchString(version) {
		return "", errors.New("version must be a numeric major.minor.patch without leading zeroes")
	}
	status, err := git(run, dir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if len(status) != 0 {
		return "", errors.New("release requires a clean Git checkout, including untracked files")
	}
	commit, err := git(run, dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	identity := strings.TrimSpace(string(commit))
	if !commitPattern.MatchString(identity) {
		return "", errors.New("git did not return a full source commit")
	}
	return identity, nil
}

func buildEnv(target string) []string {
	goos, goarch, full := strings.Cut(target, "/")
	if !full {
		goarch = "amd64"
	}
	overrides := map[string]string{"GOTOOLCHAIN": toolchain, "GOENV": "off", "GOROOT": "", "GO111MODULE": "on", "GOFIPS140": "off", "GOWORK": "off", "GOFLAGS": "", "GOEXPERIMENT": "", "CGO_ENABLED": "0", "GOAMD64": "v1", "GOARM64": "v8.0", "GOOS": goos, "GOARCH": goarch}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if _, ok := overrides[strings.ToUpper(key)]; !ok {
			env = append(env, value)
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+overrides[key])
	}
	return env
}

func parseTargets(value string) ([]string, error) {
	targets := strings.Split(value, ",")
	seen := make(map[string]bool)
	for _, target := range targets {
		if (target != "windows/amd64" && target != "linux/amd64" && target != "linux/arm64") || seen[target] {
			return nil, errors.New("targets must be unique windows/amd64, linux/amd64 or linux/arm64 entries")
		}
		seen[target] = true
	}
	return targets, nil
}

func resolveSource(run commandRunner, root, sourceRef string) (string, error) {
	if sourceRef == "" {
		return "", errors.New("source-ref must not be empty")
	}
	value, err := git(run, root, "rev-parse", "--verify", "--end-of-options", sourceRef+"^{commit}")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(value))
	if !commitPattern.MatchString(commit) {
		return "", errors.New("git did not return a full source commit")
	}
	return commit, nil
}

func release(version, output, sourceRef, targetList string, run commandRunner) (resultErr error) {
	targets, err := parseTargets(targetList)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	rootBytes, err := git(run, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(rootBytes))
	commit, err := preflight(run, root, version)
	if err != nil {
		return err
	}
	if sourceRef != "HEAD" {
		commit, err = resolveSource(run, root, sourceRef)
		if err != nil {
			return err
		}
	}
	if output == "" {
		return errors.New("--output is required")
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if err = emptyOutput(output); err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", "localpost-release-")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(temp)) }()
	snapshot := filepath.Join(temp, "source")
	source, err := git(run, root, "archive", "--format=tar", commit)
	if err != nil {
		return err
	}
	if err = extractSource(source, snapshot); err != nil {
		return err
	}
	contract, err := os.ReadFile(filepath.Join(snapshot, "openapi.json"))
	if err != nil {
		return err
	}
	var api struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err = json.Unmarshal(contract, &api); err != nil {
		return err
	}
	if api.Info.Version != version {
		return errors.New("release version differs from canonical OpenAPI info.version")
	}
	pin, err := os.ReadFile(filepath.Join(snapshot, ".go-version"))
	if err != nil {
		return err
	}
	if "go"+strings.TrimSpace(string(pin)) != toolchain {
		return errors.New("repository Go pin differs from release toolchain")
	}
	env := buildEnv("linux")
	actual, err := run(snapshot, env, "go", "env", "GOVERSION")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(actual)) != toolchain {
		return errors.New("selected Go toolchain does not match release pin")
	}
	gorootBytes, err := run(snapshot, env, "go", "env", "GOROOT")
	if err != nil {
		return err
	}
	goroot := strings.TrimSpace(string(gorootBytes))
	license, err := os.ReadFile(filepath.Join(snapshot, "LICENSE"))
	if err != nil {
		return err
	}
	sums := make([]string, 0, len(targets))
	for _, target := range targets {
		goos, goarch, _ := strings.Cut(target, "/")
		name := "localpost"
		if goos == "windows" {
			name += ".exe"
		}
		binaryPath := filepath.Join(temp, name)
		ldflags := "-X main.releaseVersion=" + version + " -X main.sourceCommit=" + commit
		_, err = run(snapshot, buildEnv(target), "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", binaryPath, "./cmd/localpost")
		if err != nil {
			return err
		}
		binary, readErr := os.ReadFile(binaryPath)
		if readErr != nil {
			return readErr
		}
		settings := map[string]string{"CGO_ENABLED": "0", "GO111MODULE": "on", "GOFIPS140": "off", "GOENV": "off", "GOEXPERIMENT": "", "GOFLAGS": "", "GOWORK": "off", "trimpath": "true", "buildvcs": "false", "ldflags": ldflags}
		if goarch == "arm64" {
			settings["GOARM64"] = "v8.0"
		} else {
			settings["GOAMD64"] = "v1"
		}
		info := manifest{version, commit, toolchain, target, settings, digest(binary)}
		metadata, marshalErr := json.MarshalIndent(info, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		metadata = append(metadata, '\n')
		entries := []entry{{name, binary, 0755}, {"README.md", []byte(runtimeReadme), 0644}, {"LICENSE", license, 0644}, {"openapi.json", contract, 0644}, {"build-info.json", metadata, 0644}}
		notices, noticeErr := goNotices(run, snapshot, goroot, target)
		if noticeErr != nil {
			return noticeErr
		}
		entries = append(entries, notices...)
		base := "localpost_" + version + "_" + goos + "_" + goarch
		archive, archiveErr := archiveBytes(base, entries, goos == "windows")
		if archiveErr != nil {
			return archiveErr
		}
		extension := ".tar.gz"
		if goos == "windows" {
			extension = ".zip"
		}
		filename := base + extension
		if err = os.WriteFile(filepath.Join(temp, filename), archive, 0644); err != nil {
			return err
		}
		sums = append(sums, digest(archive)+"  "+filename+"\n")
	}
	// Recheck before publishing outputs; never overwrite a caller's existing file.
	if err = emptyOutput(output); err != nil {
		return err
	}
	if err = os.MkdirAll(output, 0755); err != nil {
		return err
	}
	for _, sum := range sums {
		filename := strings.TrimSpace(strings.SplitN(sum, "  ", 2)[1])
		data, readErr := os.ReadFile(filepath.Join(temp, filename))
		if readErr != nil {
			return readErr
		}
		if err = writeExclusive(filepath.Join(output, filename), data); err != nil {
			return err
		}
	}
	return writeExclusive(filepath.Join(output, "SHA256SUMS"), []byte(strings.Join(sums, "")))
}

func emptyOutput(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("output must be a real directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("output directory must be empty")
	}
	return nil
}
func writeExclusive(filename string, data []byte) error {
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Close())
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func extractSource(data []byte, destination string) error {
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		// Git emits a global PAX record carrying the source commit identity.
		// It is metadata, not an extracted filesystem entry.
		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		clean := path.Clean(header.Name)
		if !filepath.IsLocal(filepath.FromSlash(clean)) || clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "\\") {
			return errors.New("unsafe source archive entry")
		}
		filename := filepath.Join(destination, filepath.FromSlash(clean))
		switch header.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(filename, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
				return err
			}
			content, readErr := io.ReadAll(reader)
			if readErr != nil {
				return readErr
			}
			if err = os.WriteFile(filename, content, 0644); err != nil {
				return err
			}
		default:
			return errors.New("source archive must contain only regular files and directories")
		}
	}
}

func goNotices(run commandRunner, source, goroot, target string) ([]entry, error) {
	deps, err := run(source, buildEnv(target), "go", "list", "-deps", "-f", "{{.Dir}}", "./cmd/localpost")
	if err != nil {
		return nil, err
	}
	files := map[string]bool{filepath.Join(goroot, "LICENSE"): true, filepath.Join(goroot, "PATENTS"): true}
	for _, dir := range strings.Split(strings.TrimSpace(string(deps)), "\n") {
		dir = strings.TrimSpace(dir)
		relative, relErr := filepath.Rel(goroot, dir)
		if relErr != nil {
			return nil, relErr
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		for dir != goroot {
			for _, name := range []string{"LICENSE", "NOTICE", "PATENTS"} {
				filename := filepath.Join(dir, name)
				info, statErr := os.Stat(filename)
				if statErr == nil && info.Mode().IsRegular() {
					files[filename] = true
				} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
					return nil, statErr
				}
			}
			dir = filepath.Dir(dir)
		}
	}
	entries := make([]entry, 0, len(files))
	for filename := range files {
		data, readErr := os.ReadFile(filename)
		if readErr != nil {
			return nil, readErr
		}
		relative, relErr := filepath.Rel(goroot, filename)
		if relErr != nil {
			return nil, relErr
		}
		entries = append(entries, entry{"licenses/go/" + filepath.ToSlash(relative), data, 0644})
	}
	return entries, nil
}

func archiveBytes(base string, entries []entry, windows bool) ([]byte, error) {
	entries = append([]entry(nil), entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	var buffer bytes.Buffer
	if windows {
		writer := zip.NewWriter(&buffer)
		for _, item := range entries {
			header := &zip.FileHeader{Name: base + "/" + item.name, Method: zip.Deflate}
			header.Modified = archiveTime
			header.SetMode(os.FileMode(item.mode))
			file, err := writer.CreateHeader(header)
			if err != nil {
				return nil, err
			}
			if _, err = file.Write(item.data); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
	} else {
		compressor := gzip.NewWriter(&buffer)
		compressor.ModTime = archiveTime
		compressor.OS = 255
		writer := tar.NewWriter(compressor)
		for _, item := range entries {
			if err := writer.WriteHeader(&tar.Header{Name: base + "/" + item.name, Mode: item.mode, Size: int64(len(item.data)), ModTime: archiveTime, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
				return nil, err
			}
			if _, err := writer.Write(item.data); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		if err := compressor.Close(); err != nil {
			return nil, err
		}
	}
	return buffer.Bytes(), nil
}

const runtimeReadme = `# localpost

A shared localhost HTTP-to-file delivery service. Support status is defined by
the matching GitHub release. Actions artifacts are candidates until accepted and
published. See build-info.json for identity
and openapi.json for the complete API contract. No Go or Node installation is
required to run this executable.

## Start and stop

Unpack the whole archive into a new directory. Run as an ordinary user:

Windows (PowerShell): .\localpost.exe
Linux: ./localpost

The default address is http://127.0.0.1:53147. Use --port 53148 to select another
port. An occupied port causes startup to fail. Use --version to inspect the
version, source commit, toolchain and target without starting the listener.
Stop with Ctrl+C (SIGINT). Shutdown stops new work and permits a network grace
period up to 30 seconds; filesystem operations do not have a hard time limit.

## Use

Create an absolute destination directory writable by the daemon's user. POST
JSON {"directory":"<absolute directory>"} to /registrations. The returned id
selects /registrations/{id}/messages: POST opaque bytes there to deliver one
complete localpost_[UUIDv7].dat file. DELETE /registrations/{id} when finished.
Registrations exist only in memory and must be recreated after a restart.
Delivered files are preserved. See openapi.json for errors and limits.

The listener is loopback-only, with permissive CORS and no authentication. Any
local process or browser page may call it. Register only intended directories;
do not expose it through a public proxy or bind it to external interfaces.

## Upgrade and rollback

Coordinate active clients and unregister their mailboxes. Stop the old process
and wait for exit. Retain its executable/archive and checksum for rollback.
Unpack the new version into a separate directory; never overwrite a running
executable. Start it on the intended port and verify readiness before clients
register again. Rollback means stopping the new process, starting the retained
version and creating fresh registrations. Neither action removes delivered files.

Verify archive SHA-256 against SHA256SUMS before use. Supported environments are
limited to the accompanying release's qualification results; a cross-compiled
candidate alone is not qualification. Bundled Go notices are under licenses/go.
`

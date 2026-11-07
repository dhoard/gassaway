// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// End-to-end tests: build the server binary, run the real server as a process,
// and talk to it the way any MCP client does — an SDK client over Streamable HTTP
// plus plain HTTP for the operator endpoints.
//
// This is the tier the design calls black box. It proves the shipped artifact
// works — the built binary, the real listener, the MCP handshake the SDK
// performs — and it reproduces failures as a command a person can retype.
package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhoard/gassaway/src/projects"
)

var serverBin string

func TestMain(m *testing.M) {
	code, err := setupAndRun(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func setupAndRun(m *testing.M) (int, error) {
	wd, err := os.Getwd()
	if err != nil {
		return 0, err
	}
	root := filepath.Join(wd, "..", "..")

	binDir, err := os.MkdirTemp("", "gassaway-e2e-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(binDir)

	serverBin = filepath.Join(binDir, "gassaway")
	build := exec.Command("go", "build", "-trimpath", "-o", serverBin, "./src/cmd/gassaway")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("building the server: %v\n%s", err, output)
	}
	return m.Run(), nil
}

// lockedBuffer collects the server's log lines while the test reads them, so the
// scanner goroutine and the assertion never share a plain bytes.Buffer.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// instance is one running server process.
type instance struct {
	url  string
	port int
	cmd  *exec.Cmd
	logs *lockedBuffer

	// waited is closed once the process has exited and waitErr is set, so the
	// process is reaped exactly once no matter how often shutdown is called.
	waited chan struct{}

	mu      sync.Mutex
	waitErr error
}

// freePort asks the kernel for a free loopback port and releases it again. The
// server refuses --port 0 by design, so a test that wants an unused port has to
// name one; the retry in startServer covers the moment between releasing the port
// and the server binding it.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// startServer runs the server binary on a free port with its own data directory
// and waits for the listening line, so tests never race the bind. The command line
// is the only configuration: nothing is inherited from the environment, which is
// what these tests are here to prove.
func startServer(t *testing.T, dataDir string) *instance {
	t.Helper()
	var last string
	for attempt := 0; attempt < 3; attempt++ {
		i, started := startOnce(t, dataDir, freePort(t))
		if started {
			t.Cleanup(func() { i.shutdown() })
			return i
		}
		last = i.logs.String()
		i.shutdown()
	}
	t.Fatalf("server did not start after 3 attempts; last output:\n%s", last)
	return nil
}

// startOnce starts one attempt and reports whether it reached the listening line.
func startOnce(t *testing.T, dataDir string, port int) (*instance, bool) {
	t.Helper()
	cmd := exec.Command(serverBin,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--project", dataDir,
	)
	logs := &lockedBuffer{}
	i := &instance{cmd: cmd, logs: logs, port: port, waited: make(chan struct{})}

	stderrPipe, err := cmd.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	go func() {
		err := cmd.Wait()
		i.mu.Lock()
		i.waitErr = err
		i.mu.Unlock()
		close(i.waited)
	}()

	addr := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = logs.Write([]byte(line + "\n"))
			if idx := strings.Index(line, "listening on "); idx >= 0 {
				rest := line[idx+len("listening on "):]
				if comma := strings.Index(rest, ","); comma > 0 {
					rest = rest[:comma]
				}
				select {
				case addr <- strings.TrimSpace(rest):
				default:
				}
			}
		}
	}()

	select {
	case a := <-addr:
		i.url = "http://" + a
		return i, true
	case <-i.waited:
		return i, false
	case <-time.After(15 * time.Second):
		return i, false
	}
}

// stop shuts the server down the way a supervisor would and reports whether it
// exited cleanly.
func (i *instance) stop(t *testing.T) {
	t.Helper()
	i.shutdown()
	assert.NoError(t, i.exitErr(), "server output:\n%s", i.logs.String())
}

// exitErr is the result of the process wait, once the process has exited.
func (i *instance) exitErr() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.waitErr
}

// shutdown is safe to call twice: an explicit stop in a test and the cleanup
// that runs afterwards must not both reap the process.
func (i *instance) shutdown() {
	select {
	case <-i.waited:
		return
	default:
	}
	_ = i.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-i.waited:
	case <-time.After(10 * time.Second):
		_ = i.cmd.Process.Kill()
		<-i.waited
	}
}

// runServer execs the server binary to completion and returns its combined output
// and exit code. It is for invocations that exit on their own: a usage error.
func runServer(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A server that wrongly starts instead of exiting must fail the test, not hang it.
	cmd := exec.CommandContext(ctx, serverBin, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running gassaway %v: %v", args, err)
		}
		return string(out), exitErr.ExitCode()
	}
	return string(out), 0
}

// endpoint is the one place the project's URL shape is written.
func endpoint(baseURL, id string) string {
	return baseURL + "/project/" + id
}

// openSession connects one MCP client to the project's endpoint and closes it
// when the test ends.
func openSession(t *testing.T, baseURL, id string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint(baseURL, id),
		DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callTool invokes one tool and returns its structured result.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, "tool %s failed: %s", name, toolText(res))
	encoded, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(encoded, &out))
	return out
}

// toolText is the textual part of a tool result, for messages the test asserts on.
func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

// createEntity writes one entity for the session's project.
func createEntity(t *testing.T, cs *mcp.ClientSession, name string, observations ...string) {
	t.Helper()
	if observations == nil {
		observations = []string{}
	}
	callTool(t, cs, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": name, "entityType": "person", "observations": observations}},
	})
}

func projectDirectory(t *testing.T, name string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(directory, 0o755))
	return directory
}

func projectID(t *testing.T, directory string) string {
	t.Helper()
	id, err := projects.Resolve(directory)
	require.NoError(t, err)
	return id
}

// The health endpoint is the only place the version is read: it is the operator's
// liveness check and the release's version query in one request.
func TestHealthReportsTheServedVersion(t *testing.T) {
	srv := startServer(t, t.TempDir())

	req, err := http.NewRequest(http.MethodGet, srv.url+"/healthz", nil)
	require.NoError(t, err)
	resp, err := httpClientDo(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var payload map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	assert.Equal(t, "ok", payload["status"])
	assert.NotEmpty(t, payload["version"], "the health payload carries the stamped version")
	assert.Equal(t, float64(0), payload["projects"])
}

// Every option is required and is written --name. A refused start must leave no
// trace: no listener, and no data directory invented on the way.
func TestMissingOptionsAreRefused(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "projects")

	for _, tc := range []struct {
		name   string
		args   []string
		option string
	}{
		{"no options at all", nil, "--host"},
		{"no port", []string{"--host", "127.0.0.1", "--project", dataDir}, "--port"},
		{"no data", []string{"--host", "127.0.0.1", "--port", "3999"}, "--project"},
		{"port zero", []string{"--host", "127.0.0.1", "--port", "0", "--project", dataDir}, "1 and 65535"},
		{"port out of range", []string{"--host", "127.0.0.1", "--port", "70000", "--project", dataDir}, "1 and 65535"},
		{"port not a number", []string{"--host", "127.0.0.1", "--port", "http", "--project", dataDir}, "1 and 65535"},
		{"blank host", []string{"--host", " ", "--port", "3999", "--project", dataDir}, "--host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runServer(t, nil, tc.args...)
			assert.Equal(t, 2, code, "a usage error exits 2; output:\n%s", out)
			assert.Contains(t, out, tc.option)
			assert.NotContains(t, out, "listening on", "a refused start must not bind")
			_, err := os.Stat(dataDir)
			assert.True(t, os.IsNotExist(err), "a refused start must not create the data directory")
		})
	}
}

// The single-dash spelling the flag package also accepts is refused, so the
// documented format is the only format.
func TestSingleDashOptionsAreRefused(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "projects")
	out, code := runServer(t, nil, "-host", "127.0.0.1", "-port", "3999", "-project", dataDir)
	assert.Equal(t, 2, code, "output:\n%s", out)
	assert.Contains(t, out, "options are written --name")
	_, err := os.Stat(dataDir)
	assert.True(t, os.IsNotExist(err), "a refused start must not create the data directory")
}

// --project is the only option that names storage; anything else is undefined.
func TestUnknownOptionIsRefused(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "project-root")
	out, code := runServer(t, nil, "--data", directory)
	require.Equal(t, 2, code, out)
	assert.Contains(t, out, "flag provided but not defined")
	_, err := os.Stat(directory)
	assert.True(t, os.IsNotExist(err), "a refused start must not create project storage")
}

// The variables the server used to read must not stand in for the required
// options: a stale unit would otherwise keep starting on an address nobody sees.
func TestEnvironmentDoesNotSupplyDefaults(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "projects")
	out, code := runServer(t, []string{
		"GASSAWAY_BIND=0.0.0.0",
		"GASSAWAY_PORT=3999",
		"GASSAWAY_PROJECTS_DIR=" + dataDir,
		"XDG_DATA_HOME=" + t.TempDir(),
	})
	assert.Equal(t, 2, code, "output:\n%s", out)
	assert.Contains(t, out, "--host")
	_, err := os.Stat(dataDir)
	assert.True(t, os.IsNotExist(err), "nothing may be created without --project")
}

// The data directory is created when it does not exist, parents included, and the
// projects live inside it.
func TestDataDirectoryIsCreated(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "deeper", "projects")
	srv := startServer(t, dataDir)

	info, err := os.Stat(dataDir)
	require.NoError(t, err, "the server must create the data directory")
	require.True(t, info.IsDir())

	project := projectDirectory(t, "app")
	createEntity(t, openSession(t, srv.url, projectID(t, project)), "Ada")

	graph := filepath.Join(dataDir, projectID(t, project), "memory.jsonl")
	_, err = os.Stat(graph)
	assert.NoError(t, err, "the project graph must live in the stated data directory")
}

func TestProjectsAreIsolated(t *testing.T) {
	dataDir := t.TempDir()
	srv := startServer(t, dataDir)
	alpha := projectDirectory(t, "app")
	beta := projectDirectory(t, "app")

	createEntity(t, openSession(t, srv.url, projectID(t, alpha)), "Ada", "writes Go")

	alphaGraph := callTool(t, openSession(t, srv.url, projectID(t, alpha)), "read_graph", map[string]any{})
	require.Len(t, alphaGraph["entities"], 1)
	assert.Equal(t, "Ada", alphaGraph["entities"].([]any)[0].(map[string]any)["name"])

	betaGraph := callTool(t, openSession(t, srv.url, projectID(t, beta)), "read_graph", map[string]any{})
	assert.Empty(t, betaGraph["entities"], "one project's writes must not reach another")

	// Storage appears for the project that was written, and only for that one: a
	// read of the other leaves the data directory untouched.
	_, err := os.Stat(filepath.Join(dataDir, projectID(t, alpha), "project.json"))
	assert.NoError(t, err, "the written project owns one directory")
	_, err = os.Stat(filepath.Join(dataDir, projectID(t, beta)))
	assert.True(t, os.IsNotExist(err), "a project that was only read must not be created")
}

func TestGraphsSurviveRestart(t *testing.T) {
	dataDir := t.TempDir()
	project := projectDirectory(t, "app")

	first := startServer(t, dataDir)
	createEntity(t, openSession(t, first.url, projectID(t, project)), "Ada")
	first.stop(t)

	second := startServer(t, dataDir)
	graph := callTool(t, openSession(t, second.url, projectID(t, project)), "read_graph", map[string]any{})
	require.Len(t, graph["entities"], 1)
	assert.Equal(t, "Ada", graph["entities"].([]any)[0].(map[string]any)["name"], "the graph must survive a restart")
}

func TestParallelClientsLoseNothing(t *testing.T) {
	srv := startServer(t, t.TempDir())
	id := projectID(t, projectDirectory(t, "app"))

	const clients = 3
	const each = 4
	var wg sync.WaitGroup
	errs := make([]error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = writeEntities(srv.url, id, i, each)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "client %d", i)
	}

	graph := callTool(t, openSession(t, srv.url, id), "read_graph", map[string]any{})
	assert.Len(t, graph["entities"], clients*each, "every concurrent write must be present")
}

// writeEntities opens one session and creates each entities in it, returning the
// first failure: a goroutine must not call require, which stops only its own test.
func writeEntities(baseURL, id string, client, each int) error {
	ctx := context.Background()
	c := mcp.NewClient(&mcp.Implementation{Name: fmt.Sprintf("e2e-%d", client), Version: "0"}, nil)
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint(baseURL, id),
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return err
	}
	defer cs.Close()

	entities := make([]map[string]any, 0, each)
	for n := 0; n < each; n++ {
		entities = append(entities, map[string]any{
			"name":         fmt.Sprintf("c%d-e%d", client, n),
			"entityType":   "person",
			"observations": []string{},
		})
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "create_entities",
		Arguments: map[string]any{"entities": entities},
	})
	if err != nil {
		return err
	}
	if res.IsError {
		return errors.New(toolText(res))
	}
	return nil
}

func TestRefusalsAndTransportErrors(t *testing.T) {
	srv := startServer(t, t.TempDir())

	// A path segment that is not a 64-hex ID is refused before any project exists.
	resp, err := httpClientDo(newJSONRequest(t, endpoint(srv.url, "not-a-project-id"), initializeBare, nil))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// An unknown tool is reported, never silently answered.
	cs := openSession(t, srv.url, projectID(t, projectDirectory(t, "app")))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool"})
	if err != nil {
		assert.Contains(t, err.Error(), "no_such_tool")
	} else {
		assert.True(t, res.IsError, "an unknown tool is an error result")
		assert.Contains(t, toolText(res), "no_such_tool")
	}

	// A stopped server is a transport error, not a hang.
	url, id := srv.url, projectID(t, projectDirectory(t, "app"))
	srv.stop(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	_, err = client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint(url, id),
		DisableStandaloneSSE: true,
	}, nil)
	assert.Error(t, err, "a stopped server must fail the connection")
}

// initializeBare is the initialize POST used where the request is refused before
// the body is read: the project has to resolve first, so the params do not matter.
const initializeBare = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

// newJSONRequest builds the POST a streamable MCP client sends.
func newJSONRequest(t *testing.T, endpoint, body string, headers map[string]string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func httpClientDo(req *http.Request) (*http.Response, error) {
	return (&http.Client{Timeout: 10 * time.Second}).Do(req)
}

// SIGTERM must stop the process cleanly even while a client holds the standalone
// SSE stream that a URL-only client opens. Before this was fixed the graceful
// wait expired, the process exited 1, and a supervisor reported every restart as
// a failure. The graph is already durable, so the client losing its stream is the
// only observable effect.
func TestSigtermExitsCleanlyWithAConnectedClient(t *testing.T) {
	srv := startServer(t, t.TempDir())
	id := projectID(t, projectDirectory(t, "app"))

	// The default transport keeps standalone SSE enabled, unlike openSession.
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: endpoint(srv.url, id),
	}, nil)
	require.NoError(t, err)
	defer cs.Close()

	createEntity(t, cs, "Ada", "durable")

	srv.stop(t) // asserts the process exited with status 0
}

// A rejected call must be visible in the server's own log, naming the project and
// the tool: that is the record an operator has when a harness reports a failure.
func TestFailedCallIsVisibleInTheServersLog(t *testing.T) {
	srv := startServer(t, t.TempDir())
	id := projectID(t, projectDirectory(t, "app"))
	cs := openSession(t, srv.url, id)

	// The flattened single-observation payload an agent sends by mistake.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "add_observations",
		Arguments: map[string]any{"entityName": "Ada", "contents": []string{"lost"}},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)

	srv.stop(t) // waits for the process, so every line has been written

	logs := srv.logs.String()
	assert.Contains(t, logs, "tool call failed")
	assert.Contains(t, logs, "tool=add_observations")
	assert.Contains(t, logs, "project="+id)
	assert.Equal(t, 1, strings.Count(logs, "tool call failed"), "one record per failed call; log was:\n%s", logs)
	assert.Contains(t, logs, "observations", "the record carries the reason the model was given")
}

// The log level is validated at start-up: a stale unit that says "verbose" fails
// loudly instead of running at a level nobody asked for.
func TestJunkLogLevelIsRefused(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "projects")
	out, code := runServer(t, []string{"GASSAWAY_LOG_LEVEL=verbose"},
		"--host", "127.0.0.1", "--port", "3999", "--project", dataDir)
	assert.Equal(t, 2, code, "output:\n%s", out)
	assert.Contains(t, out, "GASSAWAY_LOG_LEVEL")
	assert.NotContains(t, out, "listening on")
	_, err := os.Stat(dataDir)
	assert.True(t, os.IsNotExist(err), "a refused start must not create the data directory")
}

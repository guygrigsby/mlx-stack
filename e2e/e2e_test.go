package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2E_ChatCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	root := repoRoot(t)
	buildAll(t, root)

	dir := t.TempDir()
	routerPort := freePort(t)
	chatPort := freePort(t)
	sockPath := filepath.Join(dir, "admin.sock")

	fakePython := filepath.Join(dir, "fake-python")
	if err := os.WriteFile(fakePython, []byte(fmt.Sprintf(`#!/bin/sh
# Drop "-m mlx_stack.launcher_shim --engine lm"; forward the rest to fakemlx.
shift 4
exec "%s/bin/fakemlx" "$@"
`, root)), 0o755); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf(`
log_dir     = "%s"
models_root = "%s"
python_bin  = "%s"

[router]
host = "127.0.0.1"
port = %d
extra_ports = []

[chat]
default_profile  = "p1"
host             = "127.0.0.1"
port             = %d
swap_timeout_sec = 5

  [chat.profiles.p1]
  model  = "/tmp/p1"
  engine = "lm"

  [chat.profiles.p2]
  model  = "/tmp/p2"
  engine = "lm"
`, dir, dir, fakePython, routerPort, chatPort)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	mlxd := exec.Command(filepath.Join(root, "bin", "mlxd"), "run",
		"--config", cfgPath,
		"--socket", sockPath,
		"--log-level", "debug",
	)
	mlxd.Stdout = os.Stdout
	mlxd.Stderr = os.Stderr
	if err := mlxd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		mlxd.Process.Signal(os.Interrupt)
		mlxd.Wait()
	}()

	waitPort(t, "127.0.0.1", routerPort, 5*time.Second)

	req, _ := http.NewRequest("POST",
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", routerPort),
		bytes.NewReader([]byte(`{"model":"p1","messages":[{"role":"user","content":"hi"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("body: %s", body)
	}

	resp2, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/models", routerPort))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct{ ID string } `json:"data"`
	}
	json.NewDecoder(resp2.Body).Decode(&list)
	if len(list.Data) != 2 {
		t.Errorf("expected 2 models, got %+v", list.Data)
	}
}

func TestE2E_HotSwap(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	root := repoRoot(t)
	buildAll(t, root)

	dir := t.TempDir()
	routerPort := freePort(t)
	chatPort := freePort(t)
	sockPath := filepath.Join(dir, "admin.sock")

	fakePython := filepath.Join(dir, "fake-python")
	os.WriteFile(fakePython, []byte(fmt.Sprintf(`#!/bin/sh
shift 4
exec "%s/bin/fakemlx" "$@"
`, root)), 0o755)

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf(`
log_dir     = "%s"
models_root = "%s"
python_bin  = "%s"

[router]
host = "127.0.0.1"
port = %d

[chat]
default_profile  = "p1"
host             = "127.0.0.1"
port             = %d
swap_timeout_sec = 5

  [chat.profiles.p1]
  model  = "/tmp/p1"
  engine = "lm"

  [chat.profiles.p2]
  model  = "/tmp/p2"
  engine = "lm"
`, dir, dir, fakePython, routerPort, chatPort)
	os.WriteFile(cfgPath, []byte(cfg), 0o644)

	mlxd := exec.Command(filepath.Join(root, "bin", "mlxd"), "run",
		"--config", cfgPath, "--socket", sockPath, "--log-level", "debug")
	mlxd.Stdout = os.Stdout
	mlxd.Stderr = os.Stderr
	mlxd.Start()
	defer func() { mlxd.Process.Signal(os.Interrupt); mlxd.Wait() }()
	waitPort(t, "127.0.0.1", routerPort, 5*time.Second)

	do(t, routerPort, `{"model":"p1"}`)
	do(t, routerPort, `{"model":"p2"}`)
	do(t, routerPort, `{"model":"p1"}`)
}

func do(t *testing.T, port int, payload string) {
	t.Helper()
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port),
		"application/json",
		strings.NewReader(payload),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func buildAll(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command("make", "build", "fakemlx")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitPort(t *testing.T, host string, port int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", host, port))
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("port %d not listening within %s", port, d)
}

func TestE2E_TagsAlias(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	root := repoRoot(t)
	buildAll(t, root)

	dir := t.TempDir()
	routerPort := freePort(t)
	chatPort := freePort(t)
	tagsPort := freePort(t)
	sockPath := filepath.Join(dir, "admin.sock")

	fakePython := filepath.Join(dir, "fake-python")
	if err := os.WriteFile(fakePython, []byte(fmt.Sprintf(`#!/bin/sh
shift 4
exec "%s/bin/fakemlx" "$@"
`, root)), 0o755); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf(`
log_dir     = "%s"
models_root = "%s"
python_bin  = "%s"

[router]
host = "127.0.0.1"
port = %d
extra_ports = []

[chat]
default_profile  = "p1"
host             = "127.0.0.1"
port             = %d
swap_timeout_sec = 5

  [chat.profiles.p1]
  model  = "/tmp/p1"
  engine = "lm"

[tags]
host   = "127.0.0.1"
port   = %d
model  = "/tmp/qwen-tags"
engine = "vlm"
alias  = "qwen-tags"
`, dir, dir, fakePython, routerPort, chatPort, tagsPort)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	mlxd := exec.Command(filepath.Join(root, "bin", "mlxd"), "run",
		"--config", cfgPath,
		"--socket", sockPath,
		"--log-level", "debug",
	)
	mlxd.Stdout = os.Stdout
	mlxd.Stderr = os.Stderr
	if err := mlxd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		mlxd.Process.Signal(os.Interrupt)
		mlxd.Wait()
	}()

	waitPort(t, "127.0.0.1", routerPort, 10*time.Second)
	// tags is async-started; give it a moment to come up before the request.
	time.Sleep(500 * time.Millisecond)

	do(t, routerPort, `{"model":"qwen-tags","messages":[{"role":"user","content":"hi"}]}`)
}

func TestE2E_EmbedManaged(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	root := repoRoot(t)
	buildAll(t, root)

	dir := t.TempDir()
	routerPort := freePort(t)
	chatPort := freePort(t)
	embedPort := freePort(t)
	sockPath := filepath.Join(dir, "admin.sock")

	fakePython := filepath.Join(dir, "fake-python")
	if err := os.WriteFile(fakePython, []byte(fmt.Sprintf(`#!/bin/sh
shift 4
exec "%s/bin/fakemlx" "$@"
`, root)), 0o755); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf(`
log_dir     = "%s"
models_root = "%s"
python_bin  = "%s"

[router]
host = "127.0.0.1"
port = %d
extra_ports = []

[chat]
default_profile  = "p1"
host             = "127.0.0.1"
port             = %d
swap_timeout_sec = 5
  [chat.profiles.p1]
  model  = "/tmp/p1"
  engine = "lm"

[embed]
managed = true
host    = "127.0.0.1"
port    = %d
model   = "/tmp/embed"
alias   = "embed"
`, dir, dir, fakePython, routerPort, chatPort, embedPort)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	mlxd := exec.Command(filepath.Join(root, "bin", "mlxd"), "run",
		"--config", cfgPath, "--socket", sockPath, "--log-level", "debug")
	mlxd.Stdout = os.Stdout
	mlxd.Stderr = os.Stderr
	if err := mlxd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { mlxd.Process.Signal(os.Interrupt); mlxd.Wait() }()

	waitPort(t, "127.0.0.1", routerPort, 10*time.Second)
	time.Sleep(500 * time.Millisecond)

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/embeddings", routerPort),
		"application/json",
		strings.NewReader(`{"model":"embed","input":"hello"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
}

func TestE2E_EmbedExternal(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	root := repoRoot(t)
	buildAll(t, root)

	// Spin up a captive httptest server playing the role of an external embed.
	extServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[{"embedding":[0.5],"index":0}],"model":"ext","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer extServer.Close()

	dir := t.TempDir()
	routerPort := freePort(t)
	chatPort := freePort(t)
	sockPath := filepath.Join(dir, "admin.sock")

	fakePython := filepath.Join(dir, "fake-python")
	os.WriteFile(fakePython, []byte(fmt.Sprintf(`#!/bin/sh
shift 4
exec "%s/bin/fakemlx" "$@"
`, root)), 0o755)

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := fmt.Sprintf(`
log_dir     = "%s"
models_root = "%s"
python_bin  = "%s"

[router]
host = "127.0.0.1"
port = %d

[chat]
default_profile  = "p1"
host             = "127.0.0.1"
port             = %d
swap_timeout_sec = 5
  [chat.profiles.p1]
  model  = "/tmp/p1"
  engine = "lm"

[embed]
managed = false
url     = %q
alias   = "embed"
`, dir, dir, fakePython, routerPort, chatPort, extServer.URL)
	os.WriteFile(cfgPath, []byte(cfg), 0o644)

	mlxd := exec.Command(filepath.Join(root, "bin", "mlxd"), "run",
		"--config", cfgPath, "--socket", sockPath, "--log-level", "debug")
	mlxd.Stdout = os.Stdout
	mlxd.Stderr = os.Stderr
	mlxd.Start()
	defer func() { mlxd.Process.Signal(os.Interrupt); mlxd.Wait() }()
	waitPort(t, "127.0.0.1", routerPort, 5*time.Second)

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/embeddings", routerPort),
		"application/json",
		strings.NewReader(`{"model":"embed","input":"world"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
}

// Smoke-check an extracted release, optionally through a CPU emulator.
// Only empty/example/placeholder profiles are used; no cloud calls are performed.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

var binary, runner, version, commit, targetOS, targetArch string
var checks []string

func main() {
	flag.StringVar(&binary, "binary", "", "extracted executable")
	flag.StringVar(&runner, "runner", "", "optional executable prefix, e.g. qemu-aarch64-static")
	flag.StringVar(&version, "version", "0.1.2", "expected version")
	flag.StringVar(&commit, "commit", "", "expected source commit")
	flag.StringVar(&targetOS, "os", "", "expected target OS")
	flag.StringVar(&targetArch, "arch", "", "expected target architecture")
	output := flag.String("out", "", "JSON evidence path")
	flag.Parse()
	err := smoke()
	result := map[string]any{"passed": err == nil, "checks": checks, "os": targetOS, "arch": targetArch, "version": version, "commit": commit, "emulated": runner != "", "cloud_calls": false}
	if err != nil {
		result["error"] = err.Error()
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	if *output != "" {
		if e := os.WriteFile(*output, append(b, '\n'), 0644); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
	}
	fmt.Println(string(b))
	if err != nil {
		os.Exit(1)
	}
}

func cmd(args ...string) *exec.Cmd {
	if runner != "" {
		return exec.Command(runner, append([]string{binary}, args...)...)
	}
	return exec.Command(binary, args...)
}

func parse(data []byte) (map[string]any, error) {
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("invalid JSON response")
	}
	return result, nil
}

func smoke() error {
	var err error
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	root := filepath.Dir(binary)
	b, err := cmd("version", "--json").Output()
	if err != nil {
		return fmt.Errorf("version failed: %w", err)
	}
	info, err := parse(b)
	if err != nil {
		return err
	}
	for key, want := range map[string]string{"version": version, "commit": commit, "os": targetOS, "arch": targetArch} {
		if info[key] != want {
			return fmt.Errorf("version metadata mismatch: %s", key)
		}
	}
	checks = append(checks, "version/source/OS/architecture")
	for _, name := range []string{"config.empty.yaml", "config.example.yaml"} {
		b, err := cmd("validate-config", "--config", filepath.Join(root, "examples", name)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("validate %s: %w: %s", name, err, b)
		}
	}
	checks = append(checks, "empty and seven-cloud example configuration")
	if err = stdio(root); err != nil {
		return err
	}
	return httpSmoke()
}

func initialize() map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "release-smoke", "version": "1"}}}
}
func call(id int, name string, args map[string]any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
}
func checkInit(response map[string]any) error {
	r, ok := response["result"].(map[string]any)
	if !ok {
		return fmt.Errorf("initialize failed")
	}
	s, ok := r["serverInfo"].(map[string]any)
	if !ok || s["version"] != version {
		return fmt.Errorf("MCP version mismatch")
	}
	return nil
}
func checkTools(response map[string]any) error {
	r, ok := response["result"].(map[string]any)
	if !ok {
		return fmt.Errorf("tools/list failed")
	}
	list, ok := r["tools"].([]any)
	if !ok {
		return fmt.Errorf("missing tools")
	}
	var names []string
	for _, value := range list {
		tool, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid tool")
		}
		name, _ := tool["name"].(string)
		names = append(names, name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"cloud_explain", "cloud_help", "cloud_native_read", "cloud_profiles", "cloud_query", "cloud_schema"}) {
		return fmt.Errorf("unexpected MCP tool surface")
	}
	return nil
}
func checkCall(response map[string]any) error {
	r, ok := response["result"].(map[string]any)
	if !ok || r["isError"] == true {
		return fmt.Errorf("MCP help/schema call failed")
	}
	return nil
}

func stdio(root string) error {
	c := cmd("serve", "--transport", "stdio", "--config", filepath.Join(root, "examples", "config.empty.yaml"))
	input, err := c.StdinPipe()
	if err != nil {
		return err
	}
	output, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err = c.Start(); err != nil {
		return err
	}
	defer func() {
		input.Close()
		if c.Process != nil {
			_ = c.Process.Kill()
		}
		_ = c.Wait()
	}()
	scan := bufio.NewScanner(output)
	scan.Buffer(make([]byte, 4096), 4<<20)
	request := func(value map[string]any) (map[string]any, error) {
		b, _ := json.Marshal(value)
		if _, err := input.Write(append(b, '\n')); err != nil {
			return nil, err
		}
		type result struct {
			v   map[string]any
			err error
		}
		done := make(chan result, 1)
		go func() {
			if !scan.Scan() {
				done <- result{err: fmt.Errorf("stdio response missing")}
				return
			}
			v, e := parse(scan.Bytes())
			done <- result{v, e}
		}()
		select {
		case r := <-done:
			return r.v, r.err
		case <-time.After(20 * time.Second):
			return nil, fmt.Errorf("stdio response timeout")
		}
	}
	r, err := request(initialize())
	if err != nil {
		return err
	}
	if err = checkInit(r); err != nil {
		return err
	}
	_, err = input.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n"))
	if err != nil {
		return err
	}
	r, err = request(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if err != nil {
		return err
	}
	if err = checkTools(r); err != nil {
		return err
	}
	for i, name := range []string{"cloud_help", "cloud_schema", "cloud_profiles"} {
		r, err = request(call(i+3, name, map[string]any{}))
		if err != nil {
			return err
		}
		if err = checkCall(r); err != nil {
			return err
		}
	}
	checks = append(checks, "stdio MCP initialize/tools/help/schema/profiles")
	return nil
}

func httpSmoke() error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := l.Addr().String()
	l.Close()
	tmp, err := os.CreateTemp("", "mcpcloud-smoke-*.yaml")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	fmt.Fprintf(tmp, "profiles:\n  release-placeholder:\n    provider: aws\n    credential:\n      source: default\n    scopes:\n      accounts: [\"123456789012\"]\n    regions: [us-east-1]\nserver:\n  listen: %s\n  auth_token_env: MCPCLOUD_RELEASE_SMOKE_TOKEN\n", addr)
	if err = tmp.Close(); err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	c := cmd("serve", "--transport", "http", "--config", path)
	c.Env = append(os.Environ(), "MCPCLOUD_RELEASE_SMOKE_TOKEN="+token)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err = c.Start(); err != nil {
		return err
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	client := &http.Client{Timeout: 10 * time.Second}
	base := "http://" + addr
	ready := false
	for i := 0; i < 100; i++ {
		r, e := client.Get(base + "/healthz")
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		return fmt.Errorf("HTTP startup failed")
	}
	post := func(value map[string]any, auth bool, host string) (map[string]any, int, error) {
		b, _ := json.Marshal(value)
		req, _ := http.NewRequest("POST", base+"/mcp", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if host != "" {
			req.Host = host
		}
		r, e := client.Do(req)
		if e != nil {
			return nil, 0, e
		}
		defer r.Body.Close()
		data, e := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if e != nil {
			return nil, r.StatusCode, e
		}
		if r.StatusCode != 200 {
			return nil, r.StatusCode, nil
		}
		v, e := parse(data)
		return v, r.StatusCode, e
	}
	_, code, err := post(initialize(), false, "")
	if err != nil || code != 401 {
		return fmt.Errorf("HTTP missing-token gate failed")
	}
	_, code, err = post(initialize(), true, "untrusted.invalid")
	if err != nil || code != 403 {
		return fmt.Errorf("HTTP host gate failed")
	}
	r, code, err := post(initialize(), true, "")
	if err != nil || code != 200 {
		return fmt.Errorf("HTTP initialize failed")
	}
	if err = checkInit(r); err != nil {
		return err
	}
	r, code, err = post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, true, "")
	if err != nil || code != 200 {
		return fmt.Errorf("HTTP tools/list failed")
	}
	if err = checkTools(r); err != nil {
		return err
	}
	r, code, err = post(call(3, "cloud_native_read", map[string]any{"profile": "release-placeholder", "operation": "unregistered.delete", "params": map[string]any{}}), true, "")
	if err != nil || code != 200 {
		return fmt.Errorf("write-operation rejection missing")
	}
	result, ok := r["result"].(map[string]any)
	content, _ := json.Marshal(result["content"])
	if !ok || result["isError"] != true || !strings.Contains(string(content), "is not registered") {
		return fmt.Errorf("unregistered operation accepted")
	}
	checks = append(checks, "HTTP health/MCP", "missing bearer rejected", "untrusted Host rejected", "unregistered write rejected")
	return nil
}

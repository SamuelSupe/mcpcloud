// Build the three native release archives without reading deployment credentials.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type artifact struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type entry struct {
	name string
	data []byte
	mode os.FileMode
}

func main() {
	if err := build(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command(name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w: %s", filepath.Base(name), err, stderr.String())
	}
	return strings.TrimSpace(string(b)), nil
}

func build() error {
	version := flag.String("version", "", "release tag, for example v0.1.0")
	out := flag.String("out", "dist", "output directory")
	goTool := flag.String("go", "go", "Go executable")
	flag.Parse()
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$`).MatchString(*version) {
		return fmt.Errorf("--version must be a version tag")
	}
	if _, err := os.Stat("go.mod"); err != nil {
		return fmt.Errorf("run from repository root")
	}
	dirty, err := command("git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if dirty != "" {
		return fmt.Errorf("release source must be committed and clean")
	}
	commit, err := command("git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	stamp, err := command("git", "show", "-s", "--format=%cI", "HEAD")
	if err != nil {
		return err
	}
	built, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return err
	}
	built = built.UTC()
	goVersion, err := command(*goTool, "version")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		return err
	}
	files := map[string]string{
		"docs/install.zh-CN.md":        "docs/install.zh-CN.md",
		"docs/permissions.md":          "docs/permissions.md",
		"docs/dsl.md":                  "docs/dsl.md",
		"docs/release-platforms.md":    "docs/release-platforms.md",
		"RELEASE_NOTES.md":             "docs/releases/" + *version + ".md",
		"examples/config.example.yaml": "examples/config.example.yaml",
		"examples/config.empty.yaml":   "examples/config.empty.yaml",
	}
	var shared []entry
	for destination, source := range files {
		b, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		shared = append(shared, entry{destination, b, 0644})
	}
	// Query packages linked into this executable, not unrelated modules in the
	// complete module graph. Include the dependencies' distribution notices.
	modules, err := command(*goTool, "list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}", "./cmd/mcpcloud")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var names []string
	var notices []entry
	for _, line := range strings.Split(modules, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 3 || parts[0] == "mcpcloud" || seen[parts[0]] {
			continue
		}
		seen[parts[0]] = true
		names = append(names, parts[0]+" "+parts[1])
		files, err := os.ReadDir(parts[2])
		if err != nil {
			return err
		}
		found := false
		for _, file := range files {
			upper := strings.ToUpper(file.Name())
			if file.IsDir() || !(strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") || strings.HasPrefix(upper, "NOTICE")) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(parts[2], file.Name()))
			if err != nil {
				return err
			}
			notices = append(notices, entry{"licenses/" + parts[0] + "@" + parts[1] + "/" + file.Name(), data, 0644})
			found = true
		}
		if !found {
			return fmt.Errorf("dependency license not found: %s", parts[0])
		}
	}
	sort.Strings(names)
	shared = append(shared, entry{"DEPENDENCIES.txt", []byte(strings.Join(names, "\n") + "\n"), 0644})
	shared = append(shared, notices...)
	shared = append(shared, entry{"README.md", []byte("# MCPCloud " + *version + "\n\n安装与使用说明：docs/install.zh-CN.md\n\n本包不含云凭证或已启用的云账号配置。源码提交：" + commit + "。\n"), 0644})
	var artifacts []artifact
	for _, target := range []struct{ os, arch string }{{"windows", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		name := "mcpcloud_" + *version + "_" + target.os + "_" + target.arch
		work := filepath.Join(*out, "work", target.os+"_"+target.arch)
		if err := os.MkdirAll(work, 0755); err != nil {
			return err
		}
		binaryName := "mcpcloud"
		if target.os == "windows" {
			binaryName += ".exe"
		}
		binary := filepath.Join(work, binaryName)
		ld := "-s -w -X mcpcloud/internal/buildinfo.Version=" + strings.TrimPrefix(*version, "v") + " -X mcpcloud/internal/buildinfo.Commit=" + commit + " -X mcpcloud/internal/buildinfo.BuildTime=" + built.Format(time.RFC3339)
		cmd := exec.Command(*goTool, "build", "-trimpath", "-buildvcs=false", "-ldflags", ld, "-o", binary, "./cmd/mcpcloud")
		for _, pair := range os.Environ() {
			key := strings.SplitN(pair, "=", 2)[0]
			if key != "GOOS" && key != "GOARCH" && key != "CGO_ENABLED" && key != "GOAMD64" && key != "GOARM64" {
				cmd.Env = append(cmd.Env, pair)
			}
		}
		cmd.Env = append(cmd.Env, "GOOS="+target.os, "GOARCH="+target.arch, "CGO_ENABLED=0", "GOAMD64=v1", "GOARM64=v8.0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", name, err)
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			return err
		}
		entries := append([]entry{}, shared...)
		entries = append(entries, entry{binaryName, data, 0755})
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
		ext := ".tar.gz"
		if target.os == "windows" {
			ext = ".zip"
		}
		archivePath := filepath.Join(*out, name+ext)
		if err := archive(archivePath, name, entries, built, target.os == "windows"); err != nil {
			return err
		}
		hash, size, err := digest(archivePath)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact{Name: name + ext, OS: target.os, Arch: target.arch, SHA256: hash, Bytes: size})
		fmt.Println("built", name+ext)
	}
	manifest := struct {
		Tag       string     `json:"tag"`
		Commit    string     `json:"commit"`
		BuildTime string     `json:"build_time"`
		GoVersion string     `json:"go_version"`
		Artifacts []artifact `json:"artifacts"`
	}{*version, commit, built.Format(time.RFC3339), goVersion, artifacts}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "release-manifest.json"), append(b, '\n'), 0644); err != nil {
		return err
	}
	guide, err := os.ReadFile("docs/install.zh-CN.md")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "INSTALL.zh-CN.md"), guide, 0644); err != nil {
		return err
	}
	var sums strings.Builder
	for _, a := range artifacts {
		fmt.Fprintf(&sums, "%s  %s\n", a.SHA256, a.Name)
	}
	for _, file := range []string{"INSTALL.zh-CN.md", "release-manifest.json"} {
		hash, _, err := digest(filepath.Join(*out, file))
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", hash, file)
	}
	return os.WriteFile(filepath.Join(*out, "SHA256SUMS"), []byte(sums.String()), 0644)
}

func digest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func archive(path, prefix string, entries []entry, stamp time.Time, windows bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if windows {
		w := zip.NewWriter(f)
		for _, e := range entries {
			h := &zip.FileHeader{Name: prefix + "/" + e.name, Method: zip.Deflate}
			h.SetMode(e.mode)
			h.SetModTime(stamp)
			out, err := w.CreateHeader(h)
			if err != nil {
				return err
			}
			if _, err = out.Write(e.data); err != nil {
				return err
			}
		}
		return w.Close()
	}
	gz := gzip.NewWriter(f)
	gz.Header.ModTime = stamp
	w := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: prefix + "/" + e.name, Mode: int64(e.mode), Size: int64(len(e.data)), ModTime: stamp, Typeflag: tar.TypeReg}
		if err = w.WriteHeader(h); err != nil {
			return err
		}
		if _, err = w.Write(e.data); err != nil {
			return err
		}
	}
	if err = w.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// qgramm-licenses assembles the complete upstream license/notice texts from
// locally downloaded modules. It never fetches arbitrary URLs or reads secrets.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
)

type module struct {
	Path, Version, Dir string
	Main               bool
	Replace            *module
}

var licenseName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|copyright)([._-].*)?$`)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	out := flag.String("out", "", "explicit license bundle destination")
	check := flag.Bool("check", false, "verify destination matches current dependency licenses")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		return errors.New("usage: go run ./cmd/qgramm-licenses -out THIRD_PARTY_LICENSES.txt [-check]")
	}
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	raw, e := cmd.Output()
	if e != nil {
		return errors.New("go list modules failed; ensure dependencies are downloaded")
	}
	modules := []module{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var m module
		e := decoder.Decode(&m)
		if e == io.EOF {
			break
		}
		if e != nil {
			return fmt.Errorf("decode module inventory: %w", e)
		}
		if !m.Main {
			modules = append(modules, m)
		}
	}
	data, e := assemble(modules)
	if e != nil {
		return e
	}
	if *check {
		previous, e := os.ReadFile(*out)
		if e != nil {
			return e
		}
		if !bytes.Equal(previous, data) {
			return errors.New("license bundle differs; regenerate explicitly")
		}
		fmt.Println("license bundle matches downloaded dependencies")
		return nil
	}
	if e = os.MkdirAll(filepath.Dir(*out), 0755); e != nil {
		return e
	}
	if e = os.WriteFile(*out, data, 0644); e != nil {
		return e
	}
	fmt.Printf("Wrote complete license texts for %d dependency modules and Go runtime\n", len(modules))
	return nil
}

func assemble(modules []module) ([]byte, error) {
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	var bundle bytes.Buffer
	bundle.WriteString("QGramm third-party license and notice texts\n\nThis inventory covers the complete Go module graph, including optional build features.\nSome listed dependencies are absent from a particular feature-selected binary.\nTexts are copied verbatim from downloaded upstream source, without legal interpretation.\nProject license is maintained separately in LICENSE.\n\n")
	goLicense, e := os.ReadFile(filepath.Join(runtime.GOROOT(), "LICENSE"))
	if e != nil {
		return nil, errors.New("Go runtime license unavailable")
	}
	fmt.Fprintf(&bundle, "===== Go runtime %s / LICENSE =====\n", runtime.Version())
	bundle.Write(goLicense)
	bundle.WriteString("\n\n")
	for _, m := range modules {
		actual := m
		if m.Replace != nil {
			actual = *m.Replace
		}
		if actual.Dir == "" {
			return nil, fmt.Errorf("module %s is not downloaded; run go mod download before license generation", m.Path)
		}
		paths, e := licenseFiles(actual.Dir)
		if e != nil {
			return nil, fmt.Errorf("inspect module %s: %w", m.Path, e)
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("no license/notice file found for %s; investigate upstream before distribution", m.Path)
		}
		fmt.Fprintf(&bundle, "===== Module %s %s =====\n", m.Path, m.Version)
		if m.Replace != nil {
			fmt.Fprintf(&bundle, "Replacement: %s %s\n", actual.Path, actual.Version)
		}
		for _, path := range paths {
			rel, _ := filepath.Rel(actual.Dir, path)
			data, e := os.ReadFile(path)
			if e != nil {
				return nil, fmt.Errorf("read license for %s: %w", m.Path, e)
			}
			fmt.Fprintf(&bundle, "----- %s -----\n", filepath.ToSlash(rel))
			bundle.Write(data)
			bundle.WriteString("\n\n")
		}
	}
	return append(bytes.TrimRight(bundle.Bytes(), "\n"), '\n'), nil
}
func licenseFiles(root string) ([]string, error) {
	paths := []string{}
	e := filepath.WalkDir(root, func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() && licenseName.MatchString(entry.Name()) {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, e
}

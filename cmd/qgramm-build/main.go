// qgramm-build compiles only the requested feature modules. It never reads
// runtime secrets or writes deployment files without an explicit command.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mgg789/QGramm/internal/config"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: qgramm-build build|plan|compose -config qgramm.toml [-out path]")
	}
	command := args[0]
	if command != "build" && command != "plan" && command != "compose" {
		return fmt.Errorf("unknown command %q", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	path := fs.String("config", "qgramm.toml", "TOML configuration")
	out := fs.String("out", "", "binary or deployment output path")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	names := c.Features.Enabled()
	tags := make([]string, len(names))
	for i, name := range names {
		tags[i] = "qg_" + name
	}
	switch command {
	case "build":
		if *out == "" {
			*out = "bin/qgramm"
		}
		if err := os.MkdirAll(filepath.Dir(*out), 0755); err != nil {
			return err
		}
		cmd := exec.Command("go", "build", "-trimpath", "-tags", strings.Join(tags, ","), "-ldflags", "-s -w -X main.compiledFeatures="+strings.Join(names, ","), "-o", *out, "./cmd/qgramm")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	case "plan":
		plan := struct {
			Features []string        `json:"features"`
			Tags     []string        `json:"build_tags"`
			Capacity config.Capacity `json:"capacity"`
			Estimate config.Estimate `json:"estimate"`
		}{names, tags, c.Capacity, config.EstimateResources(c)}
		data, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if *out == "" {
			_, err = os.Stdout.Write(data)
			return err
		}
		return writeOutput(*out, data)
	case "compose":
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		configPath, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		configRelative, err := filepath.Rel(cwd, configPath)
		if err != nil || configRelative == ".." || strings.HasPrefix(configRelative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("compose configuration must be inside the current project build context")
		}
		host, port, _ := net.SplitHostPort(c.Server.Listen)
		if host != "" && host != "0.0.0.0" && host != "::" {
			return fmt.Errorf("compose requires server.listen on 0.0.0.0 or :: inside the container; use TLS or trusted_proxy")
		}
		for _, storagePath := range []string{c.Storage.Path, c.Storage.Files} {
			if !strings.HasPrefix(filepath.Clean(storagePath), "/data/") {
				return fmt.Errorf("compose requires storage paths beneath /data for the persistent volume")
			}
		}
		if *out == "" {
			*out = "compose.yaml"
		}
		// YAML strings use JSON quoting, a subset of valid YAML. No values from
		// secret environment variables are inspected or embedded.
		quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
		refs := []string{c.Security.TokenPublicKeyEnv, c.Security.ManagementSecretEnv, c.Security.MasterKeyEnv, c.Security.HPKEKeyEnv}
		refs = append(refs, c.Security.PreviousMasterKeyEnvs...)
		refs = append(refs, c.Security.PreviousHPKEKeyEnvs...)
		if c.Features.OpenAI {
			refs = append(refs, c.AI.OpenAIKeyEnv)
		}
		if c.Features.Anthropic {
			refs = append(refs, c.AI.AnthropicKeyEnv)
		}
		if c.Features.Calls {
			refs = append(refs, c.Calls.TURNSecretEnv)
		}
		for _, t := range c.AI.Tools {
			if t.SecretEnv != "" {
				refs = append(refs, t.SecretEnv)
			}
		}
		unique := map[string]bool{}
		var env strings.Builder
		for _, ref := range refs {
			if !unique[ref] {
				unique[ref] = true
				fmt.Fprintf(&env, "      %s: %s\n", ref, quote("${"+ref+":?required runtime secret}"))
			}
		}
		var volumes strings.Builder
		fmt.Fprintf(&volumes, "      - %s\n      - qgramm-data:/data\n", quote(configPath+":/app/qgramm.toml:ro"))
		for _, tlsPath := range []string{c.Server.TLSCert, c.Server.TLSKey} {
			if tlsPath != "" {
				source, e := filepath.Abs(tlsPath)
				if e != nil {
					return e
				}
				target := tlsPath
				if !filepath.IsAbs(target) {
					target = filepath.Join("/app", target)
				}
				fmt.Fprintf(&volumes, "      - %s\n", quote(source+":"+target+":ro"))
			}
		}
		estimate := config.EstimateResources(c)
		data := fmt.Sprintf("# Generated explicitly by qgramm-build compose. Sizing is an uncalibrated estimate.\n# Estimated CPU: %d cores; memory: %d bytes. Load test before production.\nservices:\n  qgramm:\n    build:\n      context: %s\n      args:\n        CONFIG: %s\n    restart: unless-stopped\n    cpus: %d\n    mem_limit: %d\n    command: [\"-config\", \"/app/qgramm.toml\"]\n    ports:\n      - %s\n    environment:\n%s    volumes:\n%s    read_only: true\n    tmpfs:\n      - /tmp\n    security_opt:\n      - no-new-privileges:true\n    cap_drop: [ALL]\nvolumes:\n  qgramm-data:\n", estimate.CPUs, estimate.MemoryBytes, quote(cwd), quote(configRelative), estimate.CPUs, estimate.MemoryBytes, quote("127.0.0.1:"+port+":"+port), env.String(), volumes.String())
		return writeOutput(*out, []byte(data))
	}
	return nil
}
func writeOutput(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

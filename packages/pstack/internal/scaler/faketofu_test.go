package scaler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestMain doubles as the fake `tofu` when PSTACK_FAKE_TOFU=<dir> is set. It must act before
// m.Run, which would parse `state list` as test flags.
func TestMain(m *testing.M) {
	if dir := os.Getenv("PSTACK_FAKE_TOFU"); dir != "" {
		os.Exit(fakeTofuMain(dir, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeTofu writes a wrapper that runs this test binary as Tofu with <dir> as its record:
// calls.log (one argv per line), env.log (the environment of each call), dirs.log (cwd, then
// argv), state.json (the names in state; `state list` without it fails as real tofu does). Knobs: <dir>/deleted (names a refresh drops), <dir>/fail (apply prints it and exits
// 1), <dir>/fail-<init|validate|apply|destroy|refresh> (that subcommand does; refresh is
// `apply -refresh-only`). GORACE=atexit_sleep_ms=0 drops the race runtime's 1 s exit sleep from
// every call.
func fakeTofu(t *testing.T) (path, dir string) {
	t.Helper()
	dir = t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "tofu")
	script := fmt.Sprintf("#!/bin/sh\nGORACE=atexit_sleep_ms=0 PSTACK_FAKE_TOFU=%q exec %q \"$@\"\n", dir, self)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func fakeTofuMain(dir string, args []string) int {
	appendFile(filepath.Join(dir, "calls.log"), strings.Join(args, " ")+"\n")
	appendFile(filepath.Join(dir, "env.log"), strings.Join(os.Environ(), "\n")+"\n--\n")
	cwd, _ := os.Getwd()
	appendFile(filepath.Join(dir, "dirs.log"), cwd+" "+strings.Join(args, " ")+"\n")
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	if sub == "apply" && slices.Contains(args, "-refresh-only") {
		sub = "refresh"
	}
	if b, err := os.ReadFile(filepath.Join(dir, "fail-"+sub)); err == nil {
		fmt.Print(string(b))
		return 1
	}
	statePath := filepath.Join(dir, "state.json")
	var names []string
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &names)
	}
	switch sub {
	case "apply":
		if b, err := os.ReadFile(filepath.Join(dir, "fail")); err == nil {
			fmt.Print(string(b))
			return 1
		}
		var vars struct {
			Workers map[string]any `json:"workers"`
		}
		b, err := os.ReadFile("workers.auto.tfvars.json")
		if err != nil {
			fmt.Println(err)
			return 1
		}
		if err := json.Unmarshal(b, &vars); err != nil {
			fmt.Println(err)
			return 1
		}
		names = names[:0]
		for n := range vars.Workers {
			names = append(names, n)
		}
	case "refresh":
		b, _ := os.ReadFile(filepath.Join(dir, "deleted"))
		gone := strings.Fields(string(b))
		names = slices.DeleteFunc(names, func(n string) bool { return slices.Contains(gone, n) })
	case "destroy":
		names = nil
	case "state":
		if _, err := os.Stat(statePath); err != nil {
			fmt.Println("No state file was found!") // as real tofu, before any apply wrote one
			return 1
		}
		for _, n := range names {
			fmt.Printf("hcloud_server.worker[%q]\n", n)
		}
		return 0
	default: // init, validate
		return 0
	}
	slices.Sort(names)
	b, _ := json.Marshal(names)
	if err := os.WriteFile(statePath, b, 0o600); err != nil {
		fmt.Println(err)
		return 1
	}
	return 0
}

func appendFile(path, s string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(s)
}

// Command stull drives the statechart mesh: check a machine for soundness,
// compile it to a settings.json hooks fragment, simulate it deterministically,
// or run it as the live hook dispatcher.
//
//	stull check   <machine>           # static soundness report
//	stull explain <machine> [--json]  # the machine's shape + soundness
//	stull compile <machine> [binary]  # emit settings.json hooks fragment
//	stull install <machine> [binary]  # merge those hooks into settings.json
//	stull sim     <machine>           # run the machine's demo scenarios
//	stull run --machine <machine>     # hook dispatcher (reads a hook event on stdin)
//
// `run` is what a compiled hook invokes. It reads one hook event as JSON on
// stdin, advances the machine, persists, and emits the hook protocol. The model
// call is runtime.AnthropicModel (Messages API, stdlib only); with no
// ANTHROPIC_API_KEY or on any error it returns "", so cells fail validation and
// the machine takes its fail-safe path.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/justinstimatze/stull/check"
	"github.com/justinstimatze/stull/compile"
	"github.com/justinstimatze/stull/registry"
	"github.com/justinstimatze/stull/runtime"
	"github.com/justinstimatze/stull/sim"
)

// version is overridden at release via -ldflags "-X main.version=$(git
// describe ...)" (see the Makefile). The git tag is the single source of truth;
// buildVersion falls back through the build metadata when the flag is unset.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "check":
		os.Exit(cmdCheck(os.Args[2:]))
	case "compile":
		os.Exit(cmdCompile(os.Args[2:]))
	case "explain":
		os.Exit(cmdExplain(os.Args[2:]))
	case "install":
		os.Exit(cmdInstall(os.Args[2:]))
	case "sim":
		os.Exit(cmdSim(os.Args[2:]))
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Println(buildVersion())
		os.Exit(0)
	default:
		usage()
	}
}

// buildVersion resolves the version string without a hand-maintained constant:
// the ldflags-baked value when set, else the module version of a `go install`,
// else the VCS revision of a local `go build`, else "dev".
func buildVersion() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	var rev string
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		if dirty {
			rev += "-dirty"
		}
		return rev
	}
	return version
}

func usage() {
	fmt.Fprintf(os.Stderr, "stull: a guarded statechart compiled to a Claude Code hook mesh\n\n")
	fmt.Fprintf(os.Stderr, "  stull check   <machine>\n")
	fmt.Fprintf(os.Stderr, "  stull explain <machine> [--json]            (the machine's shape + soundness)\n")
	fmt.Fprintf(os.Stderr, "  stull compile <machine> [binary-path]       (settings.json hooks fragment)\n")
	fmt.Fprintf(os.Stderr, "  stull install <machine> [binary] [--global|--project] [--write]\n")
	fmt.Fprintf(os.Stderr, "  stull sim     <machine>\n")
	fmt.Fprintf(os.Stderr, "  stull run --machine <machine>   (reads a hook event on stdin; STULL_TRACE=1 to trace)\n")
	fmt.Fprintf(os.Stderr, "  stull version\n\n")
	fmt.Fprintf(os.Stderr, "machines: %s\n", strings.Join(registry.Names(), ", "))
	os.Exit(2)
}

func lookup(name string) registry.Entry {
	e, ok := registry.Get(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown machine %q (have: %s)\n", name, strings.Join(registry.Names(), ", "))
		os.Exit(2)
	}
	return e
}

func cmdCheck(args []string) int {
	if len(args) < 1 {
		usage()
	}
	m := lookup(args[0]).Machine()
	rep := check.Inspect(m)
	if !rep.Sound() {
		fmt.Printf("UNSOUND: %q\n", m.Name)
		for _, e := range rep.Errors {
			fmt.Println("  " + e)
		}
		printWarnings(rep.Warnings)
		return 1
	}
	fmt.Printf("OK: %q compiles (%d states, fuel %d)\n", m.Name, len(m.States), m.Fuel)
	printWarnings(rep.Warnings)
	return 0
}

// printWarnings surfaces checker warnings loudly on stderr. A warned machine
// still compiles — the point is that a risky shape can never pass silently.
func printWarnings(warns []string) {
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "⚠ WARNING: "+w)
	}
}

func cmdCompile(args []string) int {
	if len(args) < 1 {
		usage()
	}
	m := lookup(args[0]).Machine()
	binary := "stull"
	if len(args) > 1 {
		binary = args[1]
	}
	out, err := compile.SettingsJSON(m, binary)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	printWarnings(check.Inspect(m).Warnings) // loud on stderr; stdout stays the fragment
	fmt.Println(out)
	return 0
}

func cmdSim(args []string) int {
	if len(args) < 1 {
		usage()
	}
	e := lookup(args[0])
	m := e.Machine()
	if e.Scenarios == nil {
		fmt.Fprintf(os.Stderr, "%q has no demo scenarios\n", m.Name)
		return 1
	}
	lints := 0
	for _, sc := range e.Scenarios() {
		steps, ctx := sim.Run(m, sc)
		fmt.Print(sim.Render(sc.Name, steps))
		fmt.Printf("  -> final state %q, fuel %d\n\n", ctx.State, ctx.Fuel)
		lints += len(sim.Lint(steps))
	}
	// A lint is a footgun a reader should never ship (e.g. a guard reading an
	// undeclared cell, silently always-false). Fail so CI/the author catches it.
	if lints > 0 {
		fmt.Fprintf(os.Stderr, "stull: %d lint warning(s) — the machine has a silent footgun; fix before shipping\n", lints)
		return 1
	}
	return 0
}

func cmdRun(args []string) int {
	// STULL_TRACE turns on a one-line stderr breadcrumb per event (default off, so
	// the hook hot path stays silent). Observability only: it cannot touch the
	// stdout hook protocol or the exit code.
	if os.Getenv("STULL_TRACE") != "" {
		runtime.Tracer = func(line string) { fmt.Fprintln(os.Stderr, line) }
	}
	name := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--machine" && i+1 < len(args) {
			name = args[i+1]
		}
	}
	e, ok := registry.Get(name)
	if !ok {
		// Never the hot path — a compiled hook always passes a valid --machine, so
		// only a hand-run or a misconfigured hook lands here. Speak on stderr so
		// the human/agent isn't left guessing at silence, but stay exit 0: a
		// misconfigured hook must never block a session.
		if name == "" {
			fmt.Fprintf(os.Stderr, "stull run: no --machine given. Use one of: %s\n", strings.Join(registry.Names(), ", "))
		} else {
			fmt.Fprintf(os.Stderr, "stull run: unknown machine %q (have: %s)\n", name, strings.Join(registry.Names(), ", "))
		}
		return 0
	}
	// The whole live-hook path is runtime.RunHook — the same one call a custom
	// binary makes. AnthropicModel is fail-safe (no key / any error -> "").
	return runtime.RunHook(e.Machine(), runtime.AnthropicModel)
}

// cmdExplain dumps a machine's shape and soundness — `--json` for the machine-
// readable form a reader (often another agent) can consume without reading Go.
func cmdExplain(args []string) int {
	jsonOut := false
	var pos []string
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
		} else {
			pos = append(pos, a)
		}
	}
	if len(pos) < 1 {
		usage()
	}
	m := lookup(pos[0]).Machine()
	d := check.Describe(m)
	if jsonOut {
		b, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	}
	sound := "sound"
	if !d.Sound {
		sound = "UNSOUND"
	}
	fmt.Printf("%s — %s, fuel %d, initial %q, triggers [%s]\n", d.Name, sound, d.Fuel, d.Initial, strings.Join(d.Triggers, " "))
	for _, c := range d.Cells {
		conf := "unconfined"
		if c.Confined {
			conf = "confined"
		}
		fmt.Printf("  cell %s (%s, %s", c.Name, c.Model, conf)
		if len(c.Needs) > 0 {
			fmt.Printf(", needs %s", strings.Join(c.Needs, "+"))
		}
		fmt.Printf(")\n")
	}
	for _, s := range d.States {
		term := ""
		if s.Terminal {
			term = " [terminal]"
		}
		fmt.Printf("  state %s%s\n", s.Name, term)
		for _, t := range s.Transitions {
			guard := "always"
			if !t.Unconditional {
				guard = "reads " + strings.Join(t.GuardReads, ",")
			}
			fmt.Printf("    on %s -> %s  (%s)  do: %s\n", t.On, t.To, guard, strings.Join(t.Effects, ","))
		}
	}
	printWarnings(d.Warnings)
	return 0
}

// cmdInstall merges a machine's hooks into a settings.json. It is a dry run by
// default — printing the target and the merged result — and only writes (with a
// .bak backup) when given --write. The merge is idempotent: re-running never
// double-registers. This closes the "I generated a fragment, now where does it
// go?" cliff with a command instead of hand-edited JSON.
func cmdInstall(args []string) int {
	write := false
	scope := "global"
	var pos []string
	for _, a := range args {
		switch a {
		case "--write":
			write = true
		case "--global":
			scope = "global"
		case "--project":
			scope = "project"
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) < 1 {
		usage()
	}
	m := lookup(pos[0]).Machine()
	binary := "stull"
	if len(pos) > 1 {
		binary = pos[1]
	}

	target, err := settingsPath(scope)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	existing, err := readSettings(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stull install: cannot read %s: %v\n", target, err)
		return 1
	}
	merged, added, err := compile.MergeHooks(existing, m, binary)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if !write {
		if added == 0 {
			fmt.Fprintf(os.Stderr, "%s already installed in %s — nothing to add. (dry run)\n", m.Name, target)
		} else {
			fmt.Fprintf(os.Stderr, "would add %d trigger entr(y/ies) for %s to %s; re-run with --write to apply. (dry run)\n", added, m.Name, target)
		}
		fmt.Println(string(b))
		return 0
	}
	if added == 0 {
		fmt.Fprintf(os.Stderr, "%s already installed in %s — nothing to do.\n", m.Name, target)
		return 0
	}
	if err := writeSettings(target, b); err != nil {
		fmt.Fprintf(os.Stderr, "stull install: write failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "installed %s into %s (%d new); backup at %s.bak\n", m.Name, target, added, target)
	return 0
}

func settingsPath(scope string) (string, error) {
	if scope == "project" {
		return filepath.Join(".claude", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// readSettings loads a settings.json into a map; a missing file is an empty map
// (not an error), so install works on a fresh machine. A present-but-malformed
// file IS an error — refusing to overwrite a file we cannot parse.
func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// writeSettings backs up any existing file to <path>.bak, then writes atomically
// via a temp file + rename, creating the parent directory if needed.
func writeSettings(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if cur, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", cur, 0o600); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

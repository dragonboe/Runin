package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"golang.org/x/sync/errgroup"
)

type config struct {
	Groups map[string][]string `json:"groups"`
}

// flags
var (
	fParallel = flag.Bool("parallel", false, "run commands concurrently")
	fJobs     = flag.Int("j", runtime.NumCPU(), "max parallel jobs")
	fDry      = flag.Bool("dry", false, "print what would run, don't actually run it")
	fDirty    = flag.Bool("dirty", false, "only target git repos with uncommitted work")
	fConfig   = flag.String("config", "", "path to config file")
	fShell    = flag.Bool("shell", false, "wrap command in sh -c / cmd /c")
	fQuiet    = flag.Bool("q", false, "suppress status lines, only show output")
)

var outMu sync.Mutex

func main() {
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	args := flag.Args()
	sep := indexOf(args, "--")
	if sep < 0 {
		die("missing '--' before command")
	}

	targets := args[:sep]
	command := args[sep+1:]
	if len(command) == 0 {
		die("no command given after '--'")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cfg := findConfig(*fConfig)
	dirs := resolve(targets, cfg)
	if len(dirs) == 0 {
		die("no directories matched")
	}

	if *fDirty {
		dirs = onlyDirty(dirs)
		if len(dirs) == 0 {
			fmt.Println("nothing dirty")
			return
		}
	}

	if !*fQuiet {
		mode := "seq"
		if *fParallel {
			mode = fmt.Sprintf("parallel, %d workers", *fJobs)
		}
		fmt.Printf("running in %d dirs (%s)\n", len(dirs), mode)
	}

	t0 := time.Now()
	ok, bad := execute(ctx, dirs, command)
	dt := time.Since(t0).Round(time.Millisecond)

	if ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "\ncancelled\n")
	}

	if !*fQuiet {
		fmt.Printf("\ndone in %s — %d ok, %d failed\n", dt, ok, len(bad))
	}

	if len(bad) > 0 {
		for _, d := range bad {
			fmt.Fprintf(os.Stderr, "  FAIL %s\n", d)
		}
		os.Exit(1)
	}
}

func execute(ctx context.Context, dirs, command []string) (int, []string) {
	var (
		mu   sync.Mutex
		ok   int
		bad  []string
	)

	limit := 1
	if *fParallel {
		limit = *fJobs
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)

	for _, d := range dirs {
		dir := d
		g.Go(func() error {
			err := run(gctx, dir, command)
			mu.Lock()
			if err != nil {
				bad = append(bad, dir)
			} else {
				ok++
			}
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()

	return ok, bad
}

func run(ctx context.Context, dir string, args []string) error {
	tag := color.CyanString("[%s]", filepath.Base(dir))

	if *fDry {
		locked(func() { fmt.Printf("%s %s\n", tag, strings.Join(args, " ")) })
		return nil
	}

	cmd := buildCmd(ctx, args)
	cmd.Dir = dir

	if !*fQuiet {
		locked(func() { fmt.Printf("%s starting\n", tag) })
	}

	// pipe both stdout and stderr so we can prefix every line
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		locked(func() { fmt.Fprintf(os.Stderr, "%s start failed: %v\n", tag, err) })
		return err
	}

	// drain both pipes concurrently
	var wg sync.WaitGroup
	drain := func(r *bufio.Scanner) {
		defer wg.Done()
		for r.Scan() {
			line := r.Text()
			locked(func() { fmt.Printf("%s %s\n", tag, line) })
		}
	}

	wg.Add(2)
	go drain(bufio.NewScanner(stdoutPipe))
	go drain(bufio.NewScanner(stderrPipe))
	wg.Wait()

	err = cmd.Wait()
	if !*fQuiet {
		locked(func() {
			if err != nil {
				if ctx.Err() != nil {
					fmt.Printf("%s interrupted\n", tag)
				} else {
					fmt.Printf("%s failed: %v\n", tag, err)
				}
			} else {
				fmt.Printf("%s done\n", tag)
			}
		})
	}
	return err
}

func buildCmd(ctx context.Context, args []string) *exec.Cmd {
	if *fShell {
		sh, shflag := shellCmd()
		return exec.CommandContext(ctx, sh, shflag, strings.Join(args, " "))
	}
	return exec.CommandContext(ctx, args[0], args[1:]...)
}

func shellCmd() (string, string) {
	if runtime.GOOS == "windows" {
		return "cmd", "/c"
	}
	return "sh", "-c"
}

// --- directory resolution ---

func resolve(patterns []string, cfg config) []string {
	seen := map[string]bool{}
	var out []string

	var walk func(string, int)
	walk = func(pat string, depth int) {
		if depth > 10 {
			return
		}

		// group reference
		if strings.HasPrefix(pat, "group:") {
			name := pat[6:]
			if entries, ok := cfg.Groups[name]; ok {
				for _, e := range entries {
					walk(e, depth+1)
				}
			}
			return
		}

		pat = expandPath(pat)
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			abs, err := filepath.Abs(m)
			if err != nil {
				continue
			}
			info, err := os.Stat(abs)
			if err != nil || !info.IsDir() {
				continue
			}
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
	}

	for _, p := range patterns {
		walk(p, 0)
	}
	return out
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") || p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[1:])
		}
	}
	return os.ExpandEnv(p)
}

// --- git filtering ---

func onlyDirty(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if gitDirty(d) {
			out = append(out, d)
		}
	}
	return out
}

func gitDirty(dir string) bool {
	// local changes?
	out, err := gitOut(dir, "status", "--porcelain", "-uno")
	if err != nil {
		return false
	}
	if len(bytes.TrimSpace(out)) > 0 {
		return true
	}

	// ahead or behind upstream?
	out, err = gitOut(dir, "rev-list", "--count", "--left-right", "@{u}...HEAD")
	if err != nil {
		return false
	}
	s := strings.TrimSpace(string(out))
	return s != "" && s != "0\t0"
}

func gitOut(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// --- config loading ---

func findConfig(explicit string) config {
	paths := []string{explicit}
	if explicit == "" {
		paths = []string{".runin.json", "runin.json"}
		if h, err := os.UserHomeDir(); err == nil {
			paths = append(paths, filepath.Join(h, ".runin.json"))
		}
	}

	for _, p := range paths {
		if p == "" {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		// strip // comments so people can annotate their config
		raw = stripLineComments(raw)
		var c config
		if json.Unmarshal(raw, &c) == nil {
			return c
		}
	}
	return config{Groups: map[string][]string{}}
}

func stripLineComments(data []byte) []byte {
	var buf bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		// only strip full-line comments to avoid breaking urls
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			buf.WriteByte('\n')
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// --- helpers ---

func indexOf(ss []string, val string) int {
	for i, s := range ss {
		if s == val {
			return i
		}
	}
	return -1
}

func locked(fn func()) {
	outMu.Lock()
	fn()
	outMu.Unlock()
}

func die(msg string) {
	fmt.Fprintf(os.Stderr, "runin: %s\n", msg)
	os.Exit(1)
}

func usage() {
	w := os.Stderr
	fmt.Fprintln(w, "Usage: runin [flags] <targets>... -- <command> [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run a command in multiple directories at once.")
	fmt.Fprintln(w, "Targets can be directory paths, globs, or group:name references.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	flag.PrintDefaults()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  runin ~/projects/* -- git pull")
	fmt.Fprintln(w, "  runin -parallel -j4 services/* -- make test")
	fmt.Fprintln(w, "  runin -dirty group:work -- git status -s")
	fmt.Fprintln(w, "  runin -shell dev/* -- 'npm install && npm test'")
}
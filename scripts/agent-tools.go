//go:build windows

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

const (
	defaultMaxLines              = 2000
	defaultMaxFileBytes          = 512 * 1024 * 1024
	defaultMaxLineBytes          = 8 * 1024 * 1024
	defaultMaxTailBytes          = 128 * 1024 * 1024
	defaultMaxTailLines          = 1_000_000
	defaultMaxReadFiles          = 1000
	jobObjectExtendedLimitInfo   = 9
	jobObjectLimitKillOnJobClose = 0x00002000
	processTerminate             = 0x0001
	processSetQuota              = 0x0100
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW   = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJob  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJob = kernel32.NewProc("AssignProcessToJobObject")
	procOpenProcess        = kernel32.NewProc("OpenProcess")
	procCloseHandle        = kernel32.NewProc("CloseHandle")
)

type ioCounters struct {
	ReadOperationCount, WriteOperationCount, OtherOperationCount uint64
	ReadTransferCount, WriteTransferCount, OtherTransferCount    uint64
}

type basicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type extendedLimitInformation struct {
	BasicLimitInformation basicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

func main() {
	os.Exit(dispatch(filepath.Base(os.Args[0]), os.Args[1:]))
}

func dispatch(executable string, args []string) int {
	name := strings.TrimSuffix(strings.ToLower(executable), ".exe")
	command := ""
	if name == "agent-read" {
		command = "read"
	} else if name == "agent-tools" {
		if len(args) == 0 {
			printMainUsage()
			return 2
		}
		command, args = args[0], args[1:]
	} else if strings.HasPrefix(name, "agent-") {
		command = strings.TrimPrefix(name, "agent-")
	} else {
		fmt.Fprintf(os.Stderr, "unsupported executable name %q\n", executable)
		return 2
	}

	switch command {
	case "read":
		return commandRead(args)
	case "rg":
		return commandRG(args)
	case "ap":
		return commandAP(args)
	case "status":
		return commandStatus(args)
	case "diff":
		return commandDiff(args)
	case "ps":
		return commandPS(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", command)
		printMainUsage()
		return 2
	}
}

func printMainUsage() {
	fmt.Fprintln(os.Stderr, "usage: agent-tools <read|rg|ap|status|diff|ps> [arguments]")
}

type readOptions struct {
	patterns    []string
	start       int
	end         int
	tail        int
	number      bool
	all         bool
	maxLines    int
	maxExplicit bool
	explicit    bool
	maxFileSize int64
	maxLineSize int
	maxTailSize int
}

type readBoundary struct {
	Index  int    `json:"index"`
	Total  int    `json:"total"`
	Path   string `json:"path"`
	Status string `json:"status,omitempty"`
}

type trackingWriter struct {
	wrote bool
	last  byte
	w     io.Writer
}

func (w *trackingWriter) Write(data []byte) (int, error) {
	n, err := w.w.Write(data)
	if n > 0 {
		w.wrote = true
		w.last = data[n-1]
	}
	return n, err
}

func commandRead(args []string) int {
	opts, err := parseReadOptions(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "usage: agent-read <path-or-pattern>... [--lines START:END | --from N --count N | --head N | --tail N | --all] [--number] [--max-lines N]")
		return 2
	}
	paths, err := resolveReadPaths(opts.patterns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-read: %v\n", err)
		return 1
	}
	return readFiles(paths, opts, os.Stdout, os.Stderr)
}

func parseReadOptions(args []string) (readOptions, error) {
	maxInt := int(^uint(0) >> 1)
	opts := readOptions{
		start:       1,
		end:         maxInt,
		tail:        -1,
		maxLines:    defaultMaxLines,
		maxFileSize: defaultMaxFileBytes,
		maxLineSize: defaultMaxLineBytes,
		maxTailSize: defaultMaxTailBytes,
	}
	var from, count int
	fromSet, countSet := false, false
	rangeOption := ""
	literalPaths := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if literalPaths {
			opts.patterns = append(opts.patterns, a)
			continue
		}
		switch a {
		case "--":
			literalPaths = true
		case "--number", "-n":
			opts.number = true
		case "--all":
			if rangeOption != "" {
				return opts, fmt.Errorf("%s cannot be combined with --all", rangeOption)
			}
			rangeOption = "--all"
			opts.all, opts.explicit = true, true
		case "--lines", "--from", "--count", "--head", "--tail", "--max-lines":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", a)
			}
			i++
			v := args[i]
			if a == "--lines" {
				if rangeOption != "" {
					return opts, fmt.Errorf("%s cannot be combined with --lines", rangeOption)
				}
				parts := strings.Split(v, ":")
				if len(parts) != 2 {
					return opts, errors.New("--lines must use START:END")
				}
				start, e1 := positiveInt(parts[0])
				end, e2 := positiveInt(parts[1])
				if e1 != nil || e2 != nil || end < start {
					return opts, errors.New("--lines requires positive START:END with END >= START")
				}
				opts.start, opts.end, opts.explicit = start, end, true
				rangeOption = "--lines"
				continue
			}
			n, e := positiveInt(v)
			if e != nil {
				return opts, fmt.Errorf("%s requires a positive integer", a)
			}
			switch a {
			case "--from":
				if rangeOption != "" && rangeOption != "--from/--count" {
					return opts, fmt.Errorf("%s cannot be combined with --from", rangeOption)
				}
				rangeOption = "--from/--count"
				from, fromSet = n, true
			case "--count":
				if rangeOption != "" && rangeOption != "--from/--count" {
					return opts, fmt.Errorf("%s cannot be combined with --count", rangeOption)
				}
				rangeOption = "--from/--count"
				count, countSet = n, true
			case "--head":
				if rangeOption != "" {
					return opts, fmt.Errorf("%s cannot be combined with --head", rangeOption)
				}
				rangeOption = "--head"
				opts.start, opts.end, opts.explicit = 1, n, true
			case "--tail":
				if rangeOption != "" {
					return opts, fmt.Errorf("%s cannot be combined with --tail", rangeOption)
				}
				if n > defaultMaxTailLines {
					return opts, fmt.Errorf("--tail exceeds the safety limit of %d lines", defaultMaxTailLines)
				}
				rangeOption = "--tail"
				opts.tail, opts.explicit = n, true
			case "--max-lines":
				opts.maxLines, opts.maxExplicit = n, true
			}
		default:
			if strings.HasPrefix(a, "-") {
				return opts, fmt.Errorf("unknown option %q", a)
			}
			opts.patterns = append(opts.patterns, a)
		}
	}
	if len(opts.patterns) == 0 {
		return opts, errors.New("at least one path or pattern is required")
	}
	if fromSet != countSet {
		return opts, errors.New("--from and --count must be used together")
	}
	if fromSet {
		if count > maxInt-from+1 {
			return opts, errors.New("--from and --count exceed the supported line range")
		}
		opts.start, opts.end, opts.explicit = from, from+count-1, true
	}
	if opts.all {
		opts.start, opts.end = 1, maxInt
	}
	if opts.explicit && !opts.maxExplicit {
		opts.maxLines = int(^uint(0) >> 1)
	}
	return opts, nil
}

func positiveInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, errors.New("not a positive integer")
	}
	return n, nil
}

var errReadLineTooLong = errors.New("line exceeds the configured byte limit")

func readLimitedLine(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	var collected []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(collected)+len(fragment) > maxBytes {
			return nil, errReadLineTooLong
		}
		if len(collected) == 0 && !errors.Is(err, bufio.ErrBufferFull) {
			return fragment, err
		}
		collected = append(collected, fragment...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return collected, err
		}
	}
}

func outputRange(r io.Reader, opts readOptions, out, errOut io.Writer, label string) int {
	reader := bufio.NewReaderSize(r, 64*1024)
	lineNo, shown := 0, 0
	for {
		line, err := readLimitedLine(reader, opts.maxLineSize)
		if errors.Is(err, errReadLineTooLong) {
			fmt.Fprintf(errOut, "agent-read: %s: line %d exceeds the %d-byte safety limit\n", label, lineNo+1, opts.maxLineSize)
			return 1
		}
		if len(line) > 0 {
			lineNo++
			if !utf8.Valid(line) {
				fmt.Fprintf(errOut, "agent-read: %s: invalid UTF-8 on line %d\n", label, lineNo)
				return 1
			}
			if lineNo >= opts.start && lineNo <= opts.end {
				if shown >= opts.maxLines {
					writeReadContinuation(errOut, label, opts, shown)
					return 0
				}
				if err := writeReadLine(out, line, lineNo, opts.number); err != nil {
					fmt.Fprintf(errOut, "agent-read: %s: output failed: %v\n", label, err)
					return 1
				}
				shown++
			}
			if lineNo >= opts.end {
				return 0
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintf(errOut, "agent-read: %s: %v\n", label, err)
				return 1
			}
			return 0
		}
	}
}

func writeReadContinuation(errOut io.Writer, label string, opts readOptions, shown int) {
	next := opts.start + shown
	count := opts.maxLines
	maxInt := int(^uint(0) >> 1)
	if opts.end != maxInt && opts.end-next+1 < count {
		count = opts.end - next + 1
	}
	if count < 1 {
		return
	}
	fmt.Fprintf(errOut, "agent-read: %s: displayed lines %d:%d; more matching content exists; continue with --from %d --count %d\n", label, opts.start, next-1, next, count)
}

type numberedLine struct {
	number int
	data   []byte
}

func outputTail(r io.Reader, opts readOptions, out, errOut io.Writer, label string) int {
	reader := bufio.NewReaderSize(r, 64*1024)
	ringSize := opts.tail
	initialCapacity := ringSize
	if initialCapacity > 4096 {
		initialCapacity = 4096
	}
	ring := make([]numberedLine, 0, initialCapacity)
	lineNo, next, retainedBytes := 0, 0, 0
	for {
		line, err := readLimitedLine(reader, opts.maxLineSize)
		if errors.Is(err, errReadLineTooLong) {
			fmt.Fprintf(errOut, "agent-read: %s: line %d exceeds the %d-byte safety limit\n", label, lineNo+1, opts.maxLineSize)
			return 1
		}
		if len(line) > 0 {
			lineNo++
			if !utf8.Valid(line) {
				fmt.Fprintf(errOut, "agent-read: %s: invalid UTF-8 on line %d\n", label, lineNo)
				return 1
			}
			if len(ring) < ringSize {
				if retainedBytes+len(line) > opts.maxTailSize {
					fmt.Fprintf(errOut, "agent-read: %s: tail buffer exceeds the %d-byte safety limit\n", label, opts.maxTailSize)
					return 1
				}
				copyLine := make([]byte, len(line))
				copy(copyLine, line)
				ring = append(ring, numberedLine{lineNo, copyLine})
				retainedBytes += len(line)
			} else if ringSize > 0 {
				oldCapacity := cap(ring[next].data)
				projectedCapacity := oldCapacity
				if projectedCapacity < len(line) {
					projectedCapacity = len(line)
				}
				if retainedBytes-oldCapacity+projectedCapacity > opts.maxTailSize {
					fmt.Fprintf(errOut, "agent-read: %s: tail buffer exceeds the %d-byte safety limit\n", label, opts.maxTailSize)
					return 1
				}
				ring[next].number = lineNo
				if oldCapacity < len(line) {
					ring[next].data = make([]byte, len(line))
				} else {
					ring[next].data = ring[next].data[:len(line)]
				}
				copy(ring[next].data, line)
				retainedBytes += projectedCapacity - oldCapacity
				next = (next + 1) % ringSize
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintf(errOut, "agent-read: %s: %v\n", label, err)
				return 1
			}
			break
		}
	}
	shown := len(ring)
	if opts.maxExplicit && opts.maxLines < shown {
		shown = opts.maxLines
	}
	for offset := 0; offset < shown; offset++ {
		index := offset
		if len(ring) == ringSize && ringSize > 0 {
			index = (next + offset) % ringSize
		}
		line := ring[index]
		if err := writeReadLine(out, line.data, line.number, opts.number); err != nil {
			fmt.Fprintf(errOut, "agent-read: %s: output failed: %v\n", label, err)
			return 1
		}
	}
	if shown < len(ring) {
		start := lineNo - opts.tail + 1
		if start < 1 {
			start = 1
		}
		nextLine := start + shown
		fmt.Fprintf(errOut, "agent-read: %s: displayed lines %d:%d; more matching content exists; continue with --from %d --count %d\n", label, start, nextLine-1, nextLine, len(ring)-shown)
	}
	return 0
}

func writeReadLine(out io.Writer, line []byte, number int, numbered bool) error {
	if numbered {
		if _, err := fmt.Fprintf(out, "%6d | ", number); err != nil {
			return err
		}
	}
	_, err := out.Write(line)
	return err
}

func readFiles(paths []string, opts readOptions, out, errOut io.Writer) int {
	batch := len(paths) > 1
	status := 0
	for index, path := range paths {
		if batch {
			writeReadBoundary(out, readBoundary{Index: index + 1, Total: len(paths), Path: path})
		}
		tracker := &trackingWriter{w: out}
		code := readOneFile(path, opts, tracker, errOut)
		if code != 0 {
			status = code
		}
		if batch {
			if tracker.wrote && tracker.last != '\n' {
				_, _ = io.WriteString(out, "\n")
			}
			endStatus := "ok"
			if code != 0 {
				endStatus = "error"
			}
			writeReadBoundaryEnd(out, readBoundary{Index: index + 1, Total: len(paths), Path: path, Status: endStatus})
		}
	}
	return status
}

func readOneFile(path string, opts readOptions, out, errOut io.Writer) int {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(errOut, "agent-read: %s: %v\n", path, err)
		return 1
	}
	if !info.Mode().IsRegular() {
		fmt.Fprintf(errOut, "agent-read: %s: not a regular file\n", path)
		return 1
	}
	if info.Size() > opts.maxFileSize {
		fmt.Fprintf(errOut, "agent-read: %s: file size %d exceeds the %d-byte safety limit\n", path, info.Size(), opts.maxFileSize)
		return 1
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(errOut, "agent-read: %s: %v\n", path, err)
		return 1
	}
	defer f.Close()
	if opts.tail >= 0 {
		return outputTail(f, opts, out, errOut, path)
	}
	return outputRange(f, opts, out, errOut, path)
}

func writeReadBoundary(out io.Writer, boundary readBoundary) {
	data, _ := json.Marshal(boundary)
	fmt.Fprintf(out, "<<<AGENT_READ_FILE_START %s>>>\n", data)
}

func writeReadBoundaryEnd(out io.Writer, boundary readBoundary) {
	data, _ := json.Marshal(boundary)
	fmt.Fprintf(out, "<<<AGENT_READ_FILE_END %s>>>\n", data)
}

func resolveReadPaths(patterns []string) ([]string, error) {
	seen := make(map[string]bool)
	var resolved []string
	for _, original := range patterns {
		pattern, err := expandUserPath(original)
		if err != nil {
			return nil, err
		}
		matches, err := resolveReadPattern(pattern)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", original, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("pattern matched no files: %s", original)
		}
		for _, match := range matches {
			absolute, err := filepath.Abs(match)
			if err != nil {
				return nil, err
			}
			absolute = filepath.Clean(absolute)
			key := strings.ToLower(absolute)
			if !seen[key] {
				if len(resolved) >= defaultMaxReadFiles {
					return nil, fmt.Errorf("matched file count exceeds the safety limit of %d", defaultMaxReadFiles)
				}
				seen[key] = true
				resolved = append(resolved, absolute)
			}
		}
	}
	return resolved, nil
}

func resolveReadPattern(pattern string) ([]string, error) {
	if !hasGlobMeta(pattern) {
		info, err := os.Stat(pattern)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("not a regular file")
		}
		return []string{pattern}, nil
	}
	if hasDoubleStarSegment(pattern) {
		return resolveRecursivePattern(pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	files := matches[:0]
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && info.Mode().IsRegular() {
			files = append(files, match)
		}
	}
	if len(files) > defaultMaxReadFiles {
		return nil, fmt.Errorf("matched file count exceeds the safety limit of %d", defaultMaxReadFiles)
	}
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i]) < strings.ToLower(files[j]) })
	return files, nil
}

func hasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func hasDoubleStarSegment(pattern string) bool {
	for _, part := range splitPathParts(pattern) {
		if part == "**" {
			return true
		}
	}
	return false
}

func resolveRecursivePattern(pattern string) ([]string, error) {
	root, relativePattern := splitRecursiveRoot(pattern)
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if matchRecursiveParts(splitPathParts(relativePattern), splitPathParts(relative)) {
			if len(matches) >= defaultMaxReadFiles {
				return fmt.Errorf("matched file count exceeds the safety limit of %d", defaultMaxReadFiles)
			}
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(matches, func(i, j int) bool { return strings.ToLower(matches[i]) < strings.ToLower(matches[j]) })
	return matches, nil
}

func splitRecursiveRoot(pattern string) (string, string) {
	cleaned := filepath.Clean(pattern)
	volume := filepath.VolumeName(cleaned)
	rest := strings.TrimPrefix(cleaned, volume)
	rooted := strings.HasPrefix(rest, string(filepath.Separator)) || strings.HasPrefix(rest, "/")
	parts := splitPathParts(strings.TrimLeft(rest, `\/`))
	firstMeta := 0
	for firstMeta < len(parts) && !hasGlobMeta(parts[firstMeta]) {
		firstMeta++
	}
	root := volume
	if rooted {
		root += string(filepath.Separator)
	}
	if firstMeta > 0 {
		root = filepath.Join(append([]string{root}, parts[:firstMeta]...)...)
	}
	if root == "" {
		root = "."
	}
	return root, filepath.Join(parts[firstMeta:]...)
}

func splitPathParts(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
}

func matchRecursiveParts(pattern, candidate []string) bool {
	if len(pattern) == 0 {
		return len(candidate) == 0
	}
	if pattern[0] == "**" {
		return matchRecursiveParts(pattern[1:], candidate) || (len(candidate) > 0 && matchRecursiveParts(pattern, candidate[1:]))
	}
	if len(candidate) == 0 {
		return false
	}
	matched, err := filepath.Match(strings.ToLower(pattern[0]), strings.ToLower(candidate[0]))
	return err == nil && matched && matchRecursiveParts(pattern[1:], candidate[1:])
}

func expandUserPath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot expand ~: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func commandRG(args []string) int {
	clean := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "" {
			clean = append(clean, arg)
		}
	}
	if len(clean) > 0 && clean[0] == "--" {
		clean = clean[1:]
	}
	if len(clean) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agent-rg <pattern> [roots...]")
		return 2
	}
	pattern, roots := clean[0], clean[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}
	rgArgs := []string{"-n", "-S", "-g", "!**/node_modules/**", "-g", "!**/dist/**", "-g", "!**/logs/**", "-g", "!**/.git/**", "-g", "!**/.idea/**", "-g", "!**/tmp/**", "-g", "!**/.cache/**", "-g", "!**/coverage/**", "--", pattern}
	rgArgs = append(rgArgs, roots...)
	return runExternal("rg.exe", rgArgs, nil)
}

func commandAP(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agent-ap <patch-file>")
		return 2
	}
	path, err := expandUserPath(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-ap: %v\n", err)
		return 1
	}
	if !utf8.Valid(data) {
		fmt.Fprintln(os.Stderr, "agent-ap: patch is not valid UTF-8")
		return 1
	}
	if !bytes.HasPrefix(data, []byte("*** Begin Patch")) {
		fmt.Fprintln(os.Stderr, "agent-ap: patch must begin with *** Begin Patch at byte zero")
		return 1
	}
	codexExe, err := findCodexExe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-ap:", err)
		return 127
	}
	return runExternal(codexExe, []string{"--codex-run-as-apply-patch", string(data)}, nil)
}

func findCodexExe() (string, error) {
	if value := os.Getenv("CODEX_EXE"); value != "" {
		if info, err := os.Stat(value); err == nil && !info.IsDir() {
			return value, nil
		}
		return "", fmt.Errorf("CODEX_EXE does not point to a file: %s", value)
	}
	local := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "OpenAI", "Codex", "bin", "codex.exe")
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		return local, nil
	}
	if path, err := exec.LookPath("codex.exe"); err == nil {
		return path, nil
	}
	return "", errors.New("Codex CLI not found; set CODEX_EXE or install Codex")
}

func commandStatus(args []string) int {
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: agent-status [repo]")
		return 2
	}
	repo := "."
	if len(args) == 1 {
		repo = args[0]
	}
	return runExternal("git.exe", []string{"-C", repo, "status", "--short"}, nil)
}

func commandDiff(args []string) int {
	repo := "."
	paths := args
	if len(args) > 0 {
		if info, err := os.Stat(args[0]); err == nil && info.IsDir() {
			repo, paths = args[0], args[1:]
		}
	}
	gitArgs := []string{"-C", repo, "diff"}
	if len(paths) == 0 {
		gitArgs = append(gitArgs, "--stat")
	} else {
		gitArgs = append(gitArgs, "--")
		gitArgs = append(gitArgs, paths...)
	}
	return runExternal("git.exe", gitArgs, nil)
}

func commandPS(args []string) int {
	implicitStdin := len(args) == 0 || args[0] == "--"
	if implicitStdin && !stdinIsRedirected() {
		fmt.Fprintln(os.Stderr, "usage: <script> | agent-ps [-- args...] | agent-ps <single-script-argument> | --stdin [-- args...] | --file <script.ps1> [-- args...]")
		return 2
	}
	powershell, err := findPowerShell()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-ps:", err)
		return 127
	}
	base := []string{"-NoProfile", "-ExecutionPolicy", "Bypass"}
	if implicitStdin {
		return runPSStdin(powershell, base, stripSeparator(args))
	}
	switch args[0] {
	case "--stdin":
		return runPSStdin(powershell, base, stripSeparator(args[1:]))
	case "--file":
		if len(args) < 2 || args[1] == "--" {
			fmt.Fprintln(os.Stderr, "agent-ps: --file requires a script path")
			return 2
		}
		extra := stripSeparator(args[2:])
		return runExternal(powershell, append(append(base, "-File", args[1]), extra...), nil)
	default:
		script, err := parsePSDirectScript(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agent-ps:", err)
			return 2
		}
		return runExternal(powershell, append(base, "-Command", script), nil)
	}
}

func stdinIsRedirected() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice == 0
}

func runPSStdin(powershell string, base, extra []string) int {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-ps: %v\n", err)
		return 1
	}
	if !utf8.Valid(data) {
		fmt.Fprintln(os.Stderr, "agent-ps: stdin is not valid UTF-8")
		return 1
	}
	if len(bytes.TrimSpace(data)) == 0 {
		fmt.Fprintln(os.Stderr, "agent-ps: stdin script is empty")
		return 2
	}
	cleanupOldPSTempFiles()
	f, err := os.CreateTemp("", "agent-ps-*.ps1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-ps: %v\n", err)
		return 1
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = f.Write(data); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-ps: %v\n", err)
		return 1
	}
	return runExternal(powershell, append(append(base, "-File", path), extra...), nil)
}

func parsePSDirectScript(args []string) (string, error) {
	if len(args) != 1 {
		return "", errors.New("direct mode accepts exactly one script argument; pipe the script to agent-ps or use --file when caller quoting may split it")
	}
	if strings.HasPrefix(args[0], "--") {
		return "", fmt.Errorf("unknown option %q", args[0])
	}
	if strings.TrimSpace(args[0]) == "" {
		return "", errors.New("a script is required")
	}
	return args[0], nil
}

func stripSeparator(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func findPowerShell() (string, error) {
	if path, err := exec.LookPath("pwsh.exe"); err == nil {
		return path, nil
	}
	return "", errors.New("PowerShell 7 (pwsh.exe) was not found")
}

func cleanupOldPSTempFiles() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "agent-ps-*.ps1"))
	cutoff := time.Now().Add(-time.Hour)
	for _, path := range matches {
		if info, err := os.Stat(path); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

func runExternal(name string, args []string, stdin io.Reader) int {
	path := name
	if !strings.ContainsAny(name, `\/`) {
		resolved, err := exec.LookPath(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s was not found in PATH\n", name)
			return 127
		}
		path = resolved
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start %s: %v\n", path, err)
		return 127
	}
	job := attachKillOnCloseJob(cmd.Process.Pid)
	err := cmd.Wait()
	if job != 0 {
		procCloseHandle.Call(job)
	}
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "%s failed: %v\n", path, err)
	return 1
}

func attachKillOnCloseJob(pid int) uintptr {
	job, _, _ := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return 0
	}
	info := extendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	ok, _, _ := procSetInformationJob.Call(job, jobObjectExtendedLimitInfo, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		procCloseHandle.Call(job)
		return 0
	}
	process, _, _ := procOpenProcess.Call(processTerminate|processSetQuota, 0, uintptr(uint32(pid)))
	if process == 0 {
		procCloseHandle.Call(job)
		return 0
	}
	ok, _, _ = procAssignProcessToJob.Call(job, process)
	procCloseHandle.Call(process)
	if ok == 0 {
		procCloseHandle.Call(job)
		return 0
	}
	return job
}

//go:build windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReadDefaultsAcceptsMultiplePaths(t *testing.T) {
	opts, err := parseReadOptions([]string{"one.txt", "two.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.patterns) != 2 || opts.start != 1 || opts.maxLines != defaultMaxLines || opts.explicit {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
}

func TestParseReadExplicitRangeIsUnboundedByDefault(t *testing.T) {
	opts, err := parseReadOptions([]string{"file.txt", "--lines", "5:9", "--number"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.start != 5 || opts.end != 9 || opts.maxLines < 100000 || !opts.number {
		t.Fatalf("unexpected range: %+v", opts)
	}
}

func TestParseReadRejectsMixedRanges(t *testing.T) {
	_, err := parseReadOptions([]string{"file.txt", "--head", "5", "--tail", "5"})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOutputRangeStopsAtRequestedEnd(t *testing.T) {
	opts, err := parseReadOptions([]string{"file.txt", "--head", "2"})
	if err != nil {
		t.Fatal(err)
	}
	input := append([]byte("one\ntwo\n"), 0xff)
	var out, errOut bytes.Buffer
	if code := outputRange(bytes.NewReader(input), opts, &out, &errOut, "file.txt"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	if got := out.String(); got != "one\ntwo\n" {
		t.Fatalf("output=%q", got)
	}
}

func TestOutputRangeReportsContinuationAfterOneLookaheadLine(t *testing.T) {
	opts, err := parseReadOptions([]string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	opts.maxLines = 2
	var out, errOut bytes.Buffer
	if code := outputRange(strings.NewReader("one\ntwo\nthree\nfour\n"), opts, &out, &errOut, "file.txt"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	if got := out.String(); got != "one\ntwo\n" {
		t.Fatalf("output=%q", got)
	}
	if !strings.Contains(errOut.String(), "--from 3 --count 2") {
		t.Fatalf("missing continuation hint: %q", errOut.String())
	}
}

func TestOutputRangeRejectsOversizedLine(t *testing.T) {
	opts, err := parseReadOptions([]string{"file.txt", "--all"})
	if err != nil {
		t.Fatal(err)
	}
	opts.maxLineSize = 4
	var out, errOut bytes.Buffer
	if code := outputRange(strings.NewReader("12345\n"), opts, &out, &errOut, "file.txt"); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(errOut.String(), "line 1 exceeds") || out.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestReadOneFileRejectsOversizedFileBeforeReading(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseReadOptions([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	opts.maxFileSize = 4
	var out, errOut bytes.Buffer
	if code := readOneFile(path, opts, &out, &errOut); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(errOut.String(), "file size 5 exceeds") || out.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestOutputTailUsesCircularOrder(t *testing.T) {
	opts, err := parseReadOptions([]string{"file.txt", "--tail", "3"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := outputTail(strings.NewReader("1\n2\n3\n4\n5\n"), opts, &out, &errOut, "file.txt"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	if got := out.String(); got != "3\n4\n5\n" {
		t.Fatalf("output=%q", got)
	}
}

func TestOutputTailEnforcesMemoryBudget(t *testing.T) {
	opts, err := parseReadOptions([]string{"file.txt", "--tail", "3"})
	if err != nil {
		t.Fatal(err)
	}
	opts.maxTailSize = 5
	var out, errOut bytes.Buffer
	if code := outputTail(strings.NewReader("aa\nbb\n"), opts, &out, &errOut, "file.txt"); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(errOut.String(), "tail buffer exceeds") {
		t.Fatalf("stderr=%q", errOut.String())
	}
}

func TestResolveReadPathsSupportsRecursiveGlobAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "a.txt"),
		filepath.Join(dir, "b.txt"),
		filepath.Join(nested, "c.txt"),
		filepath.Join(nested, "d.go"),
	} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := resolveReadPaths([]string{filepath.Join(dir, "*.txt"), filepath.Join(dir, "**", "*.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("paths=%v", paths)
	}
	if filepath.Base(paths[0]) != "a.txt" || filepath.Base(paths[1]) != "b.txt" || filepath.Base(paths[2]) != "c.txt" {
		t.Fatalf("unexpected order: %v", paths)
	}
}

func TestResolveReadPatternEnforcesFileCountLimit(t *testing.T) {
	dir := t.TempDir()
	for index := 0; index <= defaultMaxReadFiles; index++ {
		path := filepath.Join(dir, fmt.Sprintf("file-%04d.txt", index))
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := resolveReadPattern(filepath.Join(dir, "*.txt"))
	if err == nil || !strings.Contains(err.Error(), "file count exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadFilesUsesAgentBoundariesForBatch(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one.txt")
	second := filepath.Join(dir, "two.txt")
	if err := os.WriteFile(first, []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseReadOptions([]string{first, second, "--all"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := readFiles([]string{first, second}, opts, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	text := out.String()
	if strings.Count(text, "<<<AGENT_READ_FILE_START ") != 2 || strings.Count(text, "<<<AGENT_READ_FILE_END ") != 2 {
		t.Fatalf("boundaries missing: %q", text)
	}
	if !strings.Contains(text, `"status":"ok"`) || !strings.Contains(text, "one\n<<<AGENT_READ_FILE_END") {
		t.Fatalf("unexpected batch output: %q", text)
	}
}

func TestDispatchUnknownReturnsUsageError(t *testing.T) {
	if got := dispatch("agent-tools.exe", []string{"missing"}); got != 2 {
		t.Fatalf("got exit code %d", got)
	}
}

func TestFindPowerShellDoesNotFallbackToWindowsPowerShell(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "powershell.exe"), []byte("placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if path, err := findPowerShell(); err == nil {
		t.Fatalf("unexpected fallback path: %s", path)
	}
}

func TestParsePSDirectScriptPreservesOneArgument(t *testing.T) {
	script := `Write-Output 'a|b & "quote"'`
	got, err := parsePSDirectScript([]string{script})
	if err != nil {
		t.Fatal(err)
	}
	if got != script {
		t.Fatalf("got %q, want %q", got, script)
	}
}

func TestParsePSDirectScriptRejectsSplitArguments(t *testing.T) {
	_, err := parsePSDirectScript([]string{"Write-Output", "value"})
	if err == nil || !strings.Contains(err.Error(), "pipe the script to agent-ps") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParsePSDirectScriptRejectsUnknownOption(t *testing.T) {
	_, err := parsePSDirectScript([]string{"--unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown option") {
		t.Fatalf("unexpected error: %v", err)
	}
}

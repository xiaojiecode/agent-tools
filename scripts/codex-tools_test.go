//go:build windows

package main

import (
	"bytes"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateUTF8LinesReportsInvalidLine(t *testing.T) {
	line, err := validateUTF8Lines(bytes.NewReader([]byte{'o', 'k', '\n', 0xff, '\n'}))
	if err == nil || line != 2 {
		t.Fatalf("line=%d err=%v", line, err)
	}
}

func TestParseGCDefaults(t *testing.T) {
	opts, err := parseGCOptions([]string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.start != 1 || opts.maxLines != 2000 || opts.explicit {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
}

func TestParseGCExplicitRangeIsUnboundedByDefault(t *testing.T) {
	opts, err := parseGCOptions([]string{"file.txt", "--lines", "5:9", "--number"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.start != 5 || opts.end != 9 || opts.maxLines < 100000 || !opts.number {
		t.Fatalf("unexpected range: %+v", opts)
	}
}

func TestParseGCFromCount(t *testing.T) {
	opts, err := parseGCOptions([]string{"file.txt", "--from", "3", "--count", "4"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.start != 3 || opts.end != 6 {
		t.Fatalf("unexpected range: %+v", opts)
	}
}

func TestSummarizeDocUsesUnicodeCharacters(t *testing.T) {
	text := strings.Repeat("界", 241)
	got := summarizeDoc(&ast.CommentGroup{List: []*ast.Comment{{Text: "// " + text}}})
	if len([]rune(got)) != 243 || !strings.HasSuffix(got, "...") {
		t.Fatalf("unexpected summary length: %d", len([]rune(got)))
	}
}

func TestBuildOutlineFiltersExported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	source := []byte("package sample\n\ntype Public struct {\n Exported string\n hidden int\n}\ntype private int\nfunc Visible() {}\nfunc hidden() {}\n")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := buildOutline(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Declarations) != 2 {
		t.Fatalf("got %d declarations: %+v", len(out.Declarations), out.Declarations)
	}
	if len(out.Declarations[0].Members) != 1 || out.Declarations[0].Members[0].Name != "Exported" {
		t.Fatalf("unexpected members: %+v", out.Declarations[0].Members)
	}
	if out.Declarations[0].Members[0].Signature != "Exported string" {
		t.Fatalf("unexpected member signature: %q", out.Declarations[0].Members[0].Signature)
	}
	if strings.Contains(out.Declarations[0].Signature, "hidden") {
		t.Fatalf("exported signature leaked an unexported field: %q", out.Declarations[0].Signature)
	}
}

func TestDispatchUnknownReturnsUsageError(t *testing.T) {
	if got := dispatch("codex-tools.exe", []string{"missing"}); got != 2 {
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
	if err == nil || !strings.Contains(err.Error(), "pipe the script to codex-ps") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParsePSDirectScriptRejectsUnknownOption(t *testing.T) {
	_, err := parsePSDirectScript([]string{"--unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown option") {
		t.Fatalf("unexpected error: %v", err)
	}
}

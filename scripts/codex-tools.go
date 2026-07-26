//go:build windows

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

const (
	defaultMaxLines              = 2000
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
	if name == "codex-tools" {
		if len(args) == 0 {
			printMainUsage()
			return 2
		}
		command, args = args[0], args[1:]
	} else if strings.HasPrefix(name, "codex-") {
		command = strings.TrimPrefix(name, "codex-")
	} else {
		fmt.Fprintf(os.Stderr, "unsupported executable name %q\n", executable)
		return 2
	}

	switch command {
	case "gc":
		return commandGC(args)
	case "go-outline":
		return commandGoOutline(args)
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
	fmt.Fprintln(os.Stderr, "usage: codex-tools <gc|go-outline|rg|ap|status|diff|ps> [arguments]")
}

type gcOptions struct {
	path        string
	start       int
	end         int
	tail        int
	number      bool
	all         bool
	maxLines    int
	maxExplicit bool
	explicit    bool
}

func commandGC(args []string) int {
	opts, err := parseGCOptions(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "usage: codex-gc <path> [--lines START:END | --from N --count N | --head N | --tail N | --all] [--number] [--max-lines N]")
		return 2
	}

	path, err := expandUserPath(opts.path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-gc: %v\n", err)
		return 1
	}
	defer f.Close()
	if line, err := validateUTF8Lines(f); err != nil {
		if line > 0 {
			fmt.Fprintf(os.Stderr, "codex-gc: invalid UTF-8 on line %d\n", line)
		} else {
			fmt.Fprintf(os.Stderr, "codex-gc: %v\n", err)
		}
		return 1
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		fmt.Fprintf(os.Stderr, "codex-gc: %v\n", err)
		return 1
	}

	if opts.tail >= 0 {
		return outputTail(f, opts)
	}
	return outputRange(f, opts)
}

func validateUTF8Lines(r io.Reader) (int, error) {
	reader := bufio.NewReader(r)
	lineNo := 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			if !utf8.Valid(line) {
				return lineNo, errors.New("invalid UTF-8")
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, nil
			}
			return 0, err
		}
	}
}

func parseGCOptions(args []string) (gcOptions, error) {
	opts := gcOptions{start: 1, end: int(^uint(0) >> 1), tail: -1, maxLines: defaultMaxLines}
	var from, count int
	fromSet, countSet := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--number", "-n":
			opts.number = true
		case "--all":
			opts.all, opts.explicit = true, true
		case "--lines", "--from", "--count", "--head", "--tail", "--max-lines":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", a)
			}
			i++
			v := args[i]
			if a == "--lines" {
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
				continue
			}
			n, e := positiveInt(v)
			if e != nil {
				return opts, fmt.Errorf("%s requires a positive integer", a)
			}
			switch a {
			case "--from":
				from, fromSet = n, true
			case "--count":
				count, countSet = n, true
			case "--head":
				opts.start, opts.end, opts.explicit = 1, n, true
			case "--tail":
				opts.tail, opts.explicit = n, true
			case "--max-lines":
				opts.maxLines, opts.maxExplicit = n, true
			}
		default:
			if strings.HasPrefix(a, "-") {
				return opts, fmt.Errorf("unknown option %q", a)
			}
			if opts.path != "" {
				return opts, errors.New("only one path is accepted")
			}
			opts.path = a
		}
	}
	if opts.path == "" {
		return opts, errors.New("a path is required")
	}
	if fromSet != countSet {
		return opts, errors.New("--from and --count must be used together")
	}
	if fromSet {
		opts.start, opts.end, opts.explicit = from, from+count-1, true
	}
	if opts.tail >= 0 && (fromSet || opts.start != 1 || opts.end != int(^uint(0)>>1) || opts.all) {
		return opts, errors.New("--tail cannot be combined with another range option")
	}
	if opts.all {
		opts.start, opts.end = 1, int(^uint(0)>>1)
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

func outputRange(r io.Reader, opts gcOptions) int {
	reader := bufio.NewReader(r)
	lineNo, shown := 0, 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			if !utf8.Valid(line) {
				fmt.Fprintf(os.Stderr, "codex-gc: invalid UTF-8 on line %d\n", lineNo)
				return 1
			}
			if lineNo >= opts.start && lineNo <= opts.end && shown < opts.maxLines {
				writeGCLine(line, lineNo, opts.number)
				shown++
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintf(os.Stderr, "codex-gc: %v\n", err)
				return 1
			}
			break
		}
	}
	lastMatching := lineNo
	if opts.end < lastMatching {
		lastMatching = opts.end
	}
	available := 0
	if lastMatching >= opts.start {
		available = lastMatching - opts.start + 1
	}
	if shown < available {
		next := opts.start + shown
		remaining := available - shown
		fmt.Fprintf(os.Stderr, "codex-gc: displayed lines %d:%d; continue with --from %d --count %d\n", opts.start, next-1, next, remaining)
	}
	return 0
}

type numberedLine struct {
	number int
	data   []byte
}

func outputTail(r io.Reader, opts gcOptions) int {
	reader := bufio.NewReader(r)
	ringSize := opts.tail
	ring := make([]numberedLine, 0, ringSize)
	lineNo := 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			if !utf8.Valid(line) {
				fmt.Fprintf(os.Stderr, "codex-gc: invalid UTF-8 on line %d\n", lineNo)
				return 1
			}
			copyLine := append([]byte(nil), line...)
			if len(ring) < ringSize {
				ring = append(ring, numberedLine{lineNo, copyLine})
			} else if ringSize > 0 {
				copy(ring, ring[1:])
				ring[len(ring)-1] = numberedLine{lineNo, copyLine}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintf(os.Stderr, "codex-gc: %v\n", err)
				return 1
			}
			break
		}
	}
	shown := ring
	if opts.maxExplicit && opts.maxLines < len(shown) {
		shown = shown[:opts.maxLines]
	}
	for _, line := range shown {
		writeGCLine(line.data, line.number, opts.number)
	}
	if len(shown) < len(ring) {
		start := lineNo - opts.tail + 1
		if start < 1 {
			start = 1
		}
		next := start + len(shown)
		fmt.Fprintf(os.Stderr, "codex-gc: displayed lines %d:%d; continue with --from %d --count %d\n", start, next-1, next, len(ring)-len(shown))
	}
	return 0
}

func writeGCLine(line []byte, number int, numbered bool) {
	if numbered {
		fmt.Fprintf(os.Stdout, "%6d | ", number)
	}
	_, _ = os.Stdout.Write(line)
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

type outline struct {
	File         string          `json:"file"`
	Package      string          `json:"package"`
	Imports      []outlineImport `json:"imports"`
	Declarations []outlineDecl   `json:"declarations"`
}

type outlineImport struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

type outlineDecl struct {
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Signature string          `json:"signature"`
	Doc       string          `json:"doc,omitempty"`
	StartLine int             `json:"start_line"`
	EndLine   int             `json:"end_line"`
	Exported  bool            `json:"exported"`
	Members   []outlineMember `json:"members,omitempty"`
}

type outlineMember struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Signature string `json:"signature"`
	Doc       string `json:"doc,omitempty"`
	Line      int    `json:"line"`
	Exported  bool   `json:"exported"`
}

func commandGoOutline(args []string) int {
	path := ""
	exported, asJSON := false, false
	for _, a := range args {
		switch a {
		case "--exported":
			exported = true
		case "--json":
			asJSON = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "codex-go-outline: unknown option %q\n", a)
				return 2
			}
			if path != "" {
				fmt.Fprintln(os.Stderr, "codex-go-outline: exactly one Go file is required")
				return 2
			}
			path = a
		}
	}
	if path == "" || !strings.EqualFold(filepath.Ext(path), ".go") {
		fmt.Fprintln(os.Stderr, "usage: codex-go-outline <path.go> [--exported] [--json]")
		return 2
	}
	path, err := expandUserPath(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, err := buildOutline(path, exported)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-go-outline: %v\n", err)
		return 1
	}
	if asJSON {
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "codex-go-outline: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	printOutline(out)
	return 0
}

func buildOutline(path string, exportedOnly bool) (outline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return outline{}, err
	}
	if !utf8.Valid(data) {
		return outline{}, errors.New("file is not valid UTF-8")
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, data, parser.ParseComments)
	if err != nil {
		return outline{}, err
	}
	out := outline{File: path, Package: file.Name.Name, Imports: []outlineImport{}, Declarations: []outlineDecl{}}
	for _, imp := range file.Imports {
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pathValue, _ := strconv.Unquote(imp.Path.Value)
		if name == "" {
			name = filepath.Base(pathValue)
		}
		out.Imports = append(out.Imports, outlineImport{Name: name, Path: pathValue, Line: fset.Position(imp.Pos()).Line})
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			isExported := ast.IsExported(d.Name.Name)
			if exportedOnly && !isExported {
				continue
			}
			kind := "func"
			if d.Recv != nil {
				kind = "method"
			}
			copyDecl := *d
			copyDecl.Body = nil
			copyDecl.Doc = nil
			out.Declarations = append(out.Declarations, outlineDecl{
				Kind: kind, Name: d.Name.Name, Signature: formatNode(fset, &copyDecl), Doc: summarizeDoc(d.Doc),
				StartLine: fset.Position(d.Pos()).Line, EndLine: fset.Position(d.End()).Line, Exported: isExported,
			})
		case *ast.GenDecl:
			kind := strings.ToLower(d.Tok.String())
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					isExported := ast.IsExported(s.Name.Name)
					if exportedOnly && !isExported {
						continue
					}
					copySpec := *s
					copySpec.Doc, copySpec.Comment = nil, nil
					copySpec.Type = signatureType(s.Type, exportedOnly)
					gd := &ast.GenDecl{Tok: d.Tok, Specs: []ast.Spec{&copySpec}}
					od := outlineDecl{Kind: "type", Name: s.Name.Name, Signature: formatNode(fset, gd), Doc: summarizeDoc(firstDoc(s.Doc, d.Doc)), StartLine: fset.Position(s.Pos()).Line, EndLine: fset.Position(s.End()).Line, Exported: isExported}
					od.Members = collectMembers(fset, s.Type, exportedOnly)
					out.Declarations = append(out.Declarations, od)
				case *ast.ValueSpec:
					for _, name := range s.Names {
						isExported := ast.IsExported(name.Name)
						if exportedOnly && !isExported {
							continue
						}
						copySpec := *s
						copySpec.Doc, copySpec.Comment = nil, nil
						copySpec.Names = []*ast.Ident{name}
						if len(s.Values) == len(s.Names) {
							for i, candidate := range s.Names {
								if candidate == name {
									copySpec.Values = []ast.Expr{s.Values[i]}
									break
								}
							}
						}
						gd := &ast.GenDecl{Tok: d.Tok, Specs: []ast.Spec{&copySpec}}
						out.Declarations = append(out.Declarations, outlineDecl{Kind: kind, Name: name.Name, Signature: formatNode(fset, gd), Doc: summarizeDoc(firstDoc(s.Doc, d.Doc)), StartLine: fset.Position(s.Pos()).Line, EndLine: fset.Position(s.End()).Line, Exported: isExported})
					}
				}
			}
		}
	}
	return out, nil
}

func signatureType(expr ast.Expr, exportedOnly bool) ast.Expr {
	var fields *ast.FieldList
	var makeResult func(*ast.FieldList) ast.Expr
	switch t := expr.(type) {
	case *ast.StructType:
		fields = t.Fields
		makeResult = func(list *ast.FieldList) ast.Expr {
			copyType := *t
			copyType.Fields = list
			return &copyType
		}
	case *ast.InterfaceType:
		fields = t.Methods
		makeResult = func(list *ast.FieldList) ast.Expr {
			copyType := *t
			copyType.Methods = list
			return &copyType
		}
	default:
		return expr
	}
	list := &ast.FieldList{Opening: fields.Opening, Closing: fields.Closing, List: []*ast.Field{}}
	for _, field := range fields.List {
		copyField := *field
		copyField.Doc, copyField.Comment = nil, nil
		if len(field.Names) == 0 {
			if !exportedOnly || ast.IsExported(embeddedName(field.Type)) {
				list.List = append(list.List, &copyField)
			}
			continue
		}
		copyField.Names = nil
		for _, name := range field.Names {
			if !exportedOnly || ast.IsExported(name.Name) {
				copyField.Names = append(copyField.Names, name)
			}
		}
		if len(copyField.Names) > 0 {
			list.List = append(list.List, &copyField)
		}
	}
	return makeResult(list)
}

func firstDoc(a, b *ast.CommentGroup) *ast.CommentGroup {
	if a != nil {
		return a
	}
	return b
}

func collectMembers(fset *token.FileSet, expr ast.Expr, exportedOnly bool) []outlineMember {
	var fields *ast.FieldList
	kind := "field"
	switch t := expr.(type) {
	case *ast.StructType:
		fields = t.Fields
	case *ast.InterfaceType:
		fields, kind = t.Methods, "method"
	default:
		return nil
	}
	result := []outlineMember{}
	for _, field := range fields.List {
		names := field.Names
		if len(names) == 0 {
			name := embeddedName(field.Type)
			isExported := ast.IsExported(name)
			if !exportedOnly || isExported {
				result = append(result, outlineMember{Kind: "embedded", Name: name, Signature: fieldSignature(fset, field, "", "embedded"), Doc: summarizeDoc(firstDoc(field.Doc, field.Comment)), Line: fset.Position(field.Pos()).Line, Exported: isExported})
			}
			continue
		}
		for _, name := range names {
			isExported := ast.IsExported(name.Name)
			if exportedOnly && !isExported {
				continue
			}
			result = append(result, outlineMember{Kind: kind, Name: name.Name, Signature: fieldSignature(fset, field, name.Name, kind), Doc: summarizeDoc(firstDoc(field.Doc, field.Comment)), Line: fset.Position(field.Pos()).Line, Exported: isExported})
		}
	}
	return result
}

func fieldSignature(fset *token.FileSet, field *ast.Field, name, kind string) string {
	typeText := formatNode(fset, field.Type)
	var signature string
	if name == "" {
		signature = typeText
	} else if kind == "method" && strings.HasPrefix(typeText, "func") {
		signature = name + strings.TrimPrefix(typeText, "func")
	} else {
		signature = name + " " + typeText
	}
	if field.Tag != nil {
		signature += " " + field.Tag.Value
	}
	return strings.TrimSpace(signature)
}

func embeddedName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.StarExpr:
		return embeddedName(e.X)
	case *ast.IndexExpr:
		return embeddedName(e.X)
	case *ast.IndexListExpr:
		return embeddedName(e.X)
	default:
		return formatNode(token.NewFileSet(), expr)
	}
}

func formatNode(fset *token.FileSet, node any) string {
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, node); err != nil {
		return ""
	}
	return strings.TrimSpace(b.String())
}

func summarizeDoc(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	text := strings.Join(strings.Fields(doc.Text()), " ")
	runes := []rune(text)
	if len(runes) > 240 {
		return string(runes[:240]) + "..."
	}
	return text
}

func printOutline(out outline) {
	fmt.Printf("file: %s\npackage: %s\n", out.File, out.Package)
	if len(out.Imports) > 0 {
		fmt.Println("imports:")
		for _, imp := range out.Imports {
			name := ""
			if imp.Name != "" {
				name = imp.Name + " "
			}
			fmt.Printf("  L%d: %s%q\n", imp.Line, name, imp.Path)
		}
	}
	if len(out.Declarations) > 0 {
		fmt.Println("declarations:")
	}
	for _, decl := range out.Declarations {
		fmt.Printf("  %s %s L%d:%d exported=%t\n    %s\n", decl.Kind, decl.Name, decl.StartLine, decl.EndLine, decl.Exported, strings.ReplaceAll(decl.Signature, "\n", "\n    "))
		if decl.Doc != "" {
			fmt.Printf("    doc: %s\n", decl.Doc)
		}
		for _, member := range decl.Members {
			fmt.Printf("    %s %s L%d exported=%t: %s\n", member.Kind, member.Name, member.Line, member.Exported, strings.ReplaceAll(member.Signature, "\n", " "))
		}
	}
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
		fmt.Fprintln(os.Stderr, "usage: codex-rg <pattern> [roots...]")
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
		fmt.Fprintln(os.Stderr, "usage: codex-ap <patch-file>")
		return 2
	}
	path, err := expandUserPath(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-ap: %v\n", err)
		return 1
	}
	if !utf8.Valid(data) {
		fmt.Fprintln(os.Stderr, "codex-ap: patch is not valid UTF-8")
		return 1
	}
	if !bytes.HasPrefix(data, []byte("*** Begin Patch")) {
		fmt.Fprintln(os.Stderr, "codex-ap: patch must begin with *** Begin Patch at byte zero")
		return 1
	}
	codexExe, err := findCodexExe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "codex-ap:", err)
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
		fmt.Fprintln(os.Stderr, "usage: codex-status [repo]")
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
		fmt.Fprintln(os.Stderr, "usage: <script> | codex-ps [-- args...] | codex-ps <single-script-argument> | --stdin [-- args...] | --file <script.ps1> [-- args...]")
		return 2
	}
	powershell, err := findPowerShell()
	if err != nil {
		fmt.Fprintln(os.Stderr, "codex-ps:", err)
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
			fmt.Fprintln(os.Stderr, "codex-ps: --file requires a script path")
			return 2
		}
		extra := stripSeparator(args[2:])
		return runExternal(powershell, append(append(base, "-File", args[1]), extra...), nil)
	default:
		script, err := parsePSDirectScript(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "codex-ps:", err)
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
		fmt.Fprintf(os.Stderr, "codex-ps: %v\n", err)
		return 1
	}
	if !utf8.Valid(data) {
		fmt.Fprintln(os.Stderr, "codex-ps: stdin is not valid UTF-8")
		return 1
	}
	if len(bytes.TrimSpace(data)) == 0 {
		fmt.Fprintln(os.Stderr, "codex-ps: stdin script is empty")
		return 2
	}
	cleanupOldPSTempFiles()
	f, err := os.CreateTemp("", "codex-ps-*.ps1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-ps: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "codex-ps: %v\n", err)
		return 1
	}
	return runExternal(powershell, append(append(base, "-File", path), extra...), nil)
}

func parsePSDirectScript(args []string) (string, error) {
	if len(args) != 1 {
		return "", errors.New("direct mode accepts exactly one script argument; pipe the script to codex-ps or use --file when caller quoting may split it")
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
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "codex-ps-*.ps1"))
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

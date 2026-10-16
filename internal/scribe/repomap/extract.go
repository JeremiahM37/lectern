package repomap

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strconv"
	"strings"
)

// Symbol is one definition in the repository.
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	EndLine   int    `json:"end_line"`
	Signature string `json:"signature"`
	Exported  bool   `json:"exported"`
	Parent    string `json:"parent,omitempty"`
	// BodyHash identifies a function body with whitespace removed, for
	// clone detection. Empty for short bodies and non-functions.
	BodyHash string `json:"body_hash,omitempty"`

	start, end int // byte offsets in the masked source (not persisted)
}

// rawImport is an import as written in the source. It is resolved to repo
// files later, once every file is known.
type rawImport struct {
	Kind  string   `json:"kind"`
	Spec  string   `json:"spec"`
	Names []string `json:"names,omitempty"`
}

// fileInfo is everything extracted from one file. It is what the cache stores.
type fileInfo struct {
	Path    string         `json:"path"`
	Lang    string         `json:"lang"`
	Lines   int            `json:"lines"`
	Symbols []Symbol       `json:"symbols"`
	Imports []rawImport    `json:"imports"`
	Idents  map[string]int `json:"idents"`
}

// parseSource extracts symbols, imports and identifiers from one file.
func parseSource(rel string, src []byte) *fileInfo {
	lg := langFor(rel)
	if lg == nil {
		return nil
	}
	masked := mask(src, &lg.lex, false)
	fi := &fileInfo{Path: rel, Lang: lg.name, Lines: bytes.Count(src, []byte{'\n'}) + 1}
	if lg.name == "go" {
		fi.Symbols, fi.Imports = extractGo(rel, src, masked)
	} else {
		fi.Symbols = extractByRules(rel, lg, src, masked)
		fi.Imports = extractImports(lg, src)
	}
	fi.Idents = countIdents(masked)
	return fi
}

// extractGo uses go/parser for symbols and imports. Ranges come from the AST
// and the function body hash comes from the masked source.
func extractGo(rel string, src, masked []byte) ([]Symbol, []rawImport) {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if f == nil {
		return nil, nil
	}
	tf := fset.File(f.Pos())
	if tf == nil {
		return nil, nil
	}
	var imps []rawImport
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err == nil {
			imps = append(imps, rawImport{Kind: "go", Spec: p})
		}
	}
	li := newLineIndex(masked)
	var syms []Symbol
	add := func(name, kind, parent, sig string, from, to token.Pos) {
		if name == "_" || name == "" {
			return
		}
		s := Symbol{
			Name:      name,
			Kind:      kind,
			File:      rel,
			Line:      tf.Line(from),
			EndLine:   tf.Line(to),
			Signature: sig,
			Exported:  ast.IsExported(name),
			Parent:    parent,
			start:     tf.Offset(from),
			end:       tf.Offset(to),
		}
		if kind == "function" || kind == "method" {
			s.BodyHash = bodyHash(masked, s.start, s.end, li)
		}
		syms = append(syms, s)
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			kind, parent := "function", ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				parent = recvTypeName(d.Recv.List[0].Type)
			}
			cp := *d
			cp.Doc = nil
			cp.Body = nil
			var b bytes.Buffer
			_ = printer.Fprint(&b, fset, &cp)
			add(d.Name.Name, kind, parent, collapse(b.String()), d.Pos(), d.End())
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch sp.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					sig := "type " + sp.Name.Name
					if kind == "type" {
						var b bytes.Buffer
						_ = printer.Fprint(&b, fset, sp.Type)
						sig += " " + collapse(b.String())
					} else {
						sig += " " + kind
					}
					add(sp.Name.Name, kind, "", sig, sp.Pos(), sp.End())
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, id := range sp.Names {
						add(id.Name, kind, "", kind+" "+id.Name, id.Pos(), sp.End())
					}
				}
			}
		}
	}
	return syms, imps
}

func recvTypeName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// extractImports applies the language's import patterns to a source whose
// string contents are kept (module specifiers live inside strings).
func extractImports(lg *language, src []byte) []rawImport {
	if len(lg.imports) == 0 {
		return nil
	}
	m := mask(src, &lg.lex, true)
	var out []rawImport
	seen := map[string]bool{}
	for _, r := range lg.imports {
		for _, sm := range r.re.FindAllSubmatch(m, -1) {
			var names []string
			if r.names > 0 && r.names < len(sm) {
				for _, n := range strings.Split(string(sm[r.names]), ",") {
					n = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.Split(n, " as ")[0]), "\\"))
					if n != "" && n != "*" {
						names = append(names, n)
					}
				}
			}
			specs := []string{""}
			if r.group > 0 && r.group < len(sm) {
				specs = []string{string(sm[r.group])}
			}
			if r.multi {
				specs = nil
				for _, part := range strings.Split(string(sm[r.group]), ",") {
					p := strings.TrimSpace(strings.Split(strings.TrimSpace(part), " as ")[0])
					if p != "" {
						specs = append(specs, p)
					}
				}
			}
			for _, sp := range specs {
				key := r.kind + "\x00" + sp
				if sp == "" || seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, rawImport{Kind: r.kind, Spec: sp, Names: names})
			}
		}
	}
	return out
}

// countIdents returns how often each identifier appears in code (strings and
// comments are already masked). Short names and stopwords are dropped.
func countIdents(m []byte) map[string]int {
	out := map[string]int{}
	for i := 0; i < len(m); {
		if !isIdentStart(m[i]) {
			i++
			continue
		}
		j := i
		for j < len(m) && isIdentPart(m[j]) {
			j++
		}
		if j-i >= 3 && !stopIdents[string(m[i:j])] {
			out[string(m[i:j])]++
		}
		i = j
	}
	return out
}

// stopIdents are names too common to carry signal in the reference graph.
var stopIdents = words(`
the and for not with this that from func function return true false nil null none
self string int bool error data value name type var let const class import export
default package public private static void struct interface new len make append
console log print println main init test tests utils util args ctx req res err
ok out get set use key len map def end out err_ str int64 uint8 byte float number
object array boolean undefined else elif then when while break continue throw
try catch finally raise except pass yield async await impl module require include
`)

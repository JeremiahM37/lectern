package repomap

import (
	"regexp"
	"strings"
)

// importRule finds import specifiers in a source with string contents kept.
type importRule struct {
	re    *regexp.Regexp
	kind  string
	group int
	names int  // capture with imported names (Python "from x import a, b")
	multi bool // the capture is a comma-separated list
}

func tsExported(sig, name, kind string) bool {
	if strings.HasPrefix(sig, "export") {
		return true
	}
	return kind == "method" && !strings.Contains(sig, "private ") && !strings.Contains(sig, "protected ") && !strings.HasPrefix(name, "#")
}

var tsRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:export[ \t]+)?(?:default[ \t]+)?(?:declare[ \t]+)?(?:async[ \t]+)?function\b[ \t]*\*?[ \t]*(\w+)`), kind: "function"},
	{re: rx(`(?m)^[ \t]*(?:export[ \t]+)?(?:default[ \t]+)?(?:declare[ \t]+)?(?:abstract[ \t]+)?class[ \t]+(\w+)`), kind: "class", container: true},
	{re: rx(`(?m)^[ \t]*(?:export[ \t]+)?(?:declare[ \t]+)?interface[ \t]+(\w+)`), kind: "interface"},
	{re: rx(`(?m)^[ \t]*(?:export[ \t]+)?(?:declare[ \t]+)?type[ \t]+(\w+)[ \t]*(?:<[^>\n]*>)?[ \t]*=`), kind: "type"},
	{re: rx(`(?m)^[ \t]*(?:export[ \t]+)?(?:declare[ \t]+)?(?:const[ \t]+)?enum[ \t]+(\w+)`), kind: "enum"},
	{re: rx(`(?m)^[ \t]*(?:export[ \t]+)?const[ \t]+(\w+)[ \t]*(?::[^=\n]+)?=[ \t]*(?:async[ \t]+)?(?:function\b|\([^()]*\)[ \t]*(?::[^=\n]+)?=>|\w+[ \t]*=>)`), kind: "function"},
	{
		re:          rx(`(?m)^[ \t]+(?:(?:public|private|protected|static|async|override|readonly|abstract|get|set)[ \t]+)*(\w+)[ \t]*(?:<[^>\n]*>)?[ \t]*\(`),
		kind:        "function",
		methodOnly:  true,
		requireBody: true,
		inParen:     true,
		skipNames:   words("if for while switch catch function return new super with"),
	},
}

var jsImports = []importRule{
	{re: rx(`(?m)(?:^|[^\w$.])(?:import|export)\b[^;'"]*?\bfrom\s*['"]([^'"\n]+)['"]`), kind: "js", group: 1},
	{re: rx(`(?m)(?:^|[^\w$.])import\s*['"]([^'"\n]+)['"]`), kind: "js", group: 1},
	{re: rx(`\brequire\s*\(\s*['"]([^'"\n]+)['"]\s*\)`), kind: "js", group: 1},
	{re: rx(`\bimport\s*\(\s*['"]([^'"\n]+)['"]\s*\)`), kind: "js", group: 1},
}

var pyRules = []defRule{
	{re: rx(`(?m)^[ \t]*class[ \t]+(\w+)`), kind: "class", container: true},
	{re: rx(`(?m)^[ \t]*(?:async[ \t]+)?def[ \t]+(\w+)`), kind: "function"},
}

var pyImports = []importRule{
	{re: rx(`(?m)^[ \t]*from[ \t]+(\.*[\w.]*)[ \t]+import[ \t]+([^\n]+)`), kind: "py", group: 1, names: 2},
	{re: rx(`(?m)^[ \t]*import[ \t]+([\w.]+(?:[ \t]+as[ \t]+\w+)?(?:[ \t]*,[ \t]*[\w.]+(?:[ \t]+as[ \t]+\w+)?)*)`), kind: "py", group: 1, multi: true},
}

var rustRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?(?:unsafe[ \t]+)?impl\b(?:<[^{;\n]*?>)?[ \t]+(?:[\w:]+(?:<[^{;\n]*?>)?[ \t]+for[ \t]+)?&?[ \t]*(\w+)`), kind: "impl", container: true, hidden: true},
	{re: rx(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?(?:unsafe[ \t]+)?trait[ \t]+(\w+)`), kind: "trait", container: true},
	{re: rx(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?(struct|enum|union)[ \t]+(\w+)`), kindGroup: 1, nameGroup: 2},
	{re: rx(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?(?:default[ \t]+)?(?:const[ \t]+)?(?:async[ \t]+)?(?:unsafe[ \t]+)?(?:extern(?:[ \t]+"[^"\n]*")?[ \t]+)?fn[ \t]+(\w+)`), kind: "function"},
	{re: rx(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?mod[ \t]+(\w+)`), kind: "module"},
	{re: rx(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?(?:const|static)[ \t]+(?:mut[ \t]+)?(\w+)[ \t]*:`), kind: "const"},
	{re: rx(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?type[ \t]+(\w+)`), kind: "type"},
}

var rustImports = []importRule{
	{re: rx(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?mod[ \t]+(\w+)[ \t]*;`), kind: "rs-mod", group: 1},
	{re: rx(`(?m)^[ \t]*(?:pub(?:\([^)\n]*\))?[ \t]+)?use[ \t]+(crate::[\w:]+)`), kind: "rs-use", group: 1},
}

var javaRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:@\w+(?:\([^)\n]*\))?[ \t]*)*(?:(?:public|private|protected|static|final|abstract|sealed|non-sealed|strictfp)[ \t]+)*(class|interface|enum|record|@interface)[ \t]+(\w+)`), kindGroup: 1, nameGroup: 2, container: true},
	{
		re:          rx(`(?m)^[ \t]+(?:@\w+(?:\([^)\n]*\))?[ \t]*)*(?:(?:public|private|protected|static|final|abstract|synchronized|native|default|strictfp)[ \t]+)*(?:<[^>\n]+>[ \t]+)?([\w$.\[\]<>,?]+)[ \t]+(\w+)[ \t]*\(`),
		nameGroup:   2,
		guardGroup:  1,
		skipTypes:   words("return new throw else case yield assert do import package extends implements"),
		skipNames:   words("if for while switch catch synchronized return new throw"),
		kind:        "function",
		methodOnly:  true,
		requireBody: true,
		inParen:     true,
	},
}

var jvmImports = []importRule{
	{re: rx(`(?m)^[ \t]*import[ \t]+(?:static[ \t]+)?([\w.]+)`), kind: "jvm", group: 1},
}

var kotlinRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:(?:public|private|protected|internal|open|abstract|sealed|data|enum|inner|annotation|value|companion|override|final)[ \t]+)*(class|interface|object)[ \t]+(\w+)`), kindGroup: 1, nameGroup: 2, container: true},
	{re: rx(`(?m)^[ \t]*(?:(?:public|private|protected|internal|open|abstract|override|suspend|inline|operator|infix|final|tailrec|external|actual|expect)[ \t]+)*fun[ \t]+(?:<[^>\n]*>[ \t]+)?(?:[\w.]+\.)?(\w+)[ \t]*\(`), kind: "function", inParen: true},
}

var csharpRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|sealed|abstract|partial|readonly|unsafe|new|record)[ \t]+)*(class|interface|struct|enum|record)[ \t]+(\w+)`), kindGroup: 1, nameGroup: 2, container: true},
	{
		re:          rx(`(?m)^[ \t]+(?:\[[^\]\n]*\][ \t]*)*(?:(?:public|private|protected|internal|static|virtual|override|abstract|async|sealed|extern|unsafe|partial|new)[ \t]+)*([\w<>\[\],.?]+)[ \t]+(\w+)[ \t]*(?:<[^>\n]*>)?[ \t]*\(`),
		nameGroup:   2,
		guardGroup:  1,
		skipTypes:   words("return new throw else await yield case using typeof nameof sizeof default"),
		skipNames:   words("if for while switch catch using lock fixed"),
		kind:        "function",
		methodOnly:  true,
		requireBody: true,
		inParen:     true,
	},
}

var swiftRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:(?:public|private|internal|fileprivate|open|final|indirect)[ \t]+)*(class|struct|enum|protocol|extension|actor)[ \t]+(\w+)`), kindGroup: 1, nameGroup: 2, container: true, skipNames: words("func var let init")},
	{re: rx(`(?m)^[ \t]*(?:(?:public|private|internal|fileprivate|open|static|class|final|override|mutating|nonmutating|convenience|required|@\w+)[ \t]+)*func[ \t]+(\w+)`), kind: "function"},
}

var cRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:template[ \t]*<[^>\n]*>[ \t]*)?(?:typedef[ \t]+)?(struct|class|union|enum(?:[ \t]+class)?)[ \t]+(\w+)[^;{}\n]*\{`), kindGroup: 1, nameGroup: 2, container: true, braceEnd: true},
	{
		re:        rx(`(?m)^[ \t]*(?:[A-Za-z_][\w:<>,*&~ \t]*?[ \t*&]+)?(~?[A-Za-z_]\w*(?:::~?[A-Za-z_]\w*)*)[ \t]*\(`),
		kind:      "function",
		custom:    cFuncScan,
		splitName: true,
		skipNames: words("if for while switch catch return sizeof else do defined decltype alignof typeid static_assert throw new delete co_return"),
	},
}

func cExported(sig, name, kind string) bool {
	return !strings.HasPrefix(sig, "static") && !strings.Contains(sig, " static ")
}

var cImports = []importRule{
	{re: rx(`(?m)^[ \t]*#[ \t]*include[ \t]*"([^"\n]+)"`), kind: "c", group: 1},
}

var rubyRules = []defRule{
	{re: rx(`(?m)^[ \t]*(class|module)[ \t]+([\w:]+)`), kindGroup: 1, nameGroup: 2, container: true},
	{re: rx(`(?m)^[ \t]*def[ \t]+(?:self\.)?([A-Za-z_]\w*[?!=]?)`), kind: "function", endless: true},
}

var rubyImports = []importRule{
	{re: rx(`(?m)^[ \t]*require_relative[ \t]+['"]([^'"\n]+)['"]`), kind: "rb-rel", group: 1},
	{re: rx(`(?m)^[ \t]*require[ \t]+['"]([^'"\n]+)['"]`), kind: "rb", group: 1},
}

var phpRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:(?:abstract|final|readonly)[ \t]+)*(class|interface|trait|enum)[ \t]+(\w+)`), kindGroup: 1, nameGroup: 2, container: true},
	{re: rx(`(?m)^[ \t]*(?:(?:public|private|protected|static|final|abstract)[ \t]+)*function[ \t]+&?(\w+)[ \t]*\(`), kind: "function", inParen: true, requireBody: true},
}

var phpImports = []importRule{
	{re: rx(`(?m)^[ \t]*use[ \t]+([\w\\]+)[ \t]*;`), kind: "php", group: 1},
}

var luaRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:local[ \t]+)?function[ \t]+([A-Za-z_][\w.:]*)`), kind: "function", splitName: true},
	{re: rx(`(?m)^[ \t]*(?:local[ \t]+)?([A-Za-z_][\w.]*)[ \t]*=[ \t]*function\b`), kind: "function", splitName: true},
}

var luaImports = []importRule{
	{re: rx(`\brequire[ \t]*\(?[ \t]*['"]([\w.]+)['"]`), kind: "lua", group: 1},
}

var shellRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:function[ \t]+)?([A-Za-z_][\w.-]*)[ \t]*\([ \t]*\)[ \t]*\{`), kind: "function", braceEnd: true},
	{re: rx(`(?m)^[ \t]*function[ \t]+([A-Za-z_][\w.-]*)[ \t]*\{`), kind: "function", braceEnd: true},
}

var shellImports = []importRule{
	{re: rx(`(?m)^[ \t]*(?:source|\.)[ \t]+['"]?([^'"\s;]+)`), kind: "sh", group: 1},
}

var zigRules = []defRule{
	{re: rx(`(?m)^[ \t]*(?:pub[ \t]+)?(?:export[ \t]+|inline[ \t]+)?fn[ \t]+(\w+)`), kind: "function"},
	{re: rx(`(?m)^[ \t]*(?:pub[ \t]+)?const[ \t]+(\w+)[ \t]*=[ \t]*(?:packed[ \t]+|extern[ \t]+)?(struct|enum|union)\b`), nameGroup: 1, kindGroup: 2, container: true},
}

var zigImports = []importRule{
	{re: rx(`@import\([ \t]*"([^"\n]+)"[ \t]*\)`), kind: "zig", group: 1},
}

var elixirRules = []defRule{
	{re: rx(`(?m)^[ \t]*defmodule[ \t]+([\w.]+)`), kind: "module", container: true},
	{re: rx(`(?m)^[ \t]*(?:defp?|defmacrop?|defguardp?)[ \t]+([a-z_]\w*[?!]?)`), kind: "function"},
}

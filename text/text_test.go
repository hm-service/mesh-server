package text

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// placeholderPattern 是测试自己的占位符识别：不复用 text.go 的 placeholderAt，
// 否则它坏了这一层也跟着坏。
var placeholderPattern = regexp.MustCompile(`\{(\d+)\}`)

// readLang 读出并解析一份清单。清单里出现重复 key 或空 value 时 parse 会报错，
// 所以读得过这两份文件本身就已经排除了这两种情况。
func readLang(t *testing.T, name string) map[string]string {
	t.Helper()
	raw, err := files.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	table, err := parse(name, string(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return table
}

func readBothLangs(t *testing.T) (chinese, english map[string]string) {
	t.Helper()
	return readLang(t, chineseFile), readLang(t, englishFile)
}

func TestLanguageFilesHaveTheSameKeys(t *testing.T) {
	chinese, english := readBothLangs(t)
	for id := range chinese {
		if _, ok := english[id]; !ok {
			t.Errorf("english.lang has no %s", id)
		}
	}
	for id := range english {
		if _, ok := chinese[id]; !ok {
			t.Errorf("chinese.lang has no %s", id)
		}
	}
	if len(chinese) != len(english) {
		t.Fatalf("chinese.lang has %d entries, english.lang has %d", len(chinese), len(english))
	}
}

func TestLanguageFilesAreUtf8WithoutBom(t *testing.T) {
	for _, name := range []string{chineseFile, englishFile} {
		raw, err := files.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !utf8.Valid(raw) {
			t.Fatalf("%s is not valid UTF-8", name)
		}
		if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
			t.Fatalf("%s starts with a BOM", name)
		}
		if bytes.ContainsRune(raw, '\r') {
			t.Fatalf("%s contains a CR: the files use LF", name)
		}
	}
}

func TestPlaceholdersAreNumberedFromZero(t *testing.T) {
	for _, name := range []string{chineseFile, englishFile} {
		table := readLang(t, name)
		for id, value := range table {
			indexes, err := placeholderIndexes(value)
			if err != nil {
				t.Errorf("%s: %s: %v", name, id, err)
				continue
			}
			if err := numberedFromZero(indexes); err != nil {
				t.Errorf("%s: %s: %v", name, id, err)
			}
		}
	}
}

// placeholderIndexes 按出现顺序返回 value 里的占位符编号，并拒绝任何不属于
// {数字} 的花括号。
func placeholderIndexes(value string) ([]int, error) {
	matches := placeholderPattern.FindAllStringSubmatchIndex(value, -1)
	indexes := make([]int, 0, len(matches))
	var rest strings.Builder
	last := 0
	for _, match := range matches {
		rest.WriteString(value[last:match[0]])
		last = match[1]
		index, err := strconv.Atoi(value[match[2]:match[3]])
		if err != nil {
			return nil, fmt.Errorf("placeholder %q is not a number", value[match[0]:match[1]])
		}
		indexes = append(indexes, index)
	}
	rest.WriteString(value[last:])
	if strings.ContainsAny(rest.String(), "{}") {
		return nil, fmt.Errorf("illegal braces in %q", value)
	}
	return indexes, nil
}

// numberedFromZero 要求编号去重后正好是 0..n-1：跳号会让实参与文案错位。
func numberedFromZero(indexes []int) error {
	seen := make(map[int]bool, len(indexes))
	for _, index := range indexes {
		seen[index] = true
	}
	unique := make([]int, 0, len(seen))
	for index := range seen {
		unique = append(unique, index)
	}
	sort.Ints(unique)
	for want, got := range unique {
		if got != want {
			return fmt.Errorf("placeholders are %v, want 0..%d", unique, len(unique)-1)
		}
	}
	return nil
}

// idsFromSource 从 ids.go 读出 id 到常量名的映射。常量表是清单的唯一引用入口，
// 两个方向都要对得上；从源文件读而不是手写列表，新增条目漏登记时才会失败。
func idsFromSource(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "ids.go", nil, 0)
	if err != nil {
		t.Fatalf("parse ids.go: %v", err)
	}
	ids := make(map[string]string)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s is not a string literal", name.Name)
				}
				id, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: %v", name.Name, err)
				}
				if other, duplicate := ids[id]; duplicate {
					t.Fatalf("%s and %s share the id %q", other, name.Name, id)
				}
				ids[id] = name.Name
			}
		}
	}
	if len(ids) == 0 {
		t.Fatal("ids.go declares no constants")
	}
	return ids
}

func TestIDsCoverBothLanguages(t *testing.T) {
	chinese, english := readBothLangs(t)
	ids := idsFromSource(t)
	for id, name := range ids {
		if _, ok := chinese[id]; !ok {
			t.Errorf("chinese.lang has no %s (%s)", id, name)
		}
		if _, ok := english[id]; !ok {
			t.Errorf("english.lang has no %s (%s)", id, name)
		}
	}
	for id := range chinese {
		if _, ok := ids[id]; !ok {
			t.Errorf("chinese.lang: %q is not referenced by a constant in ids.go", id)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		name string
		id   string
		args []any
		want string
	}{
		{
			name: "no placeholder",
			id:   APIInvalidRequestBody,
			want: "请求内容不合法",
		},
		{
			name: "one placeholder",
			id:   AppNotesTooLong,
			args: []any{64},
			want: "appNotes 最长 64 字符",
		},
		{
			name: "two placeholders",
			id:   AppPackageManifestInvalid,
			args: []any{"version.json", "version \"bad\" is not YYYY.MM.DD-N"},
			want: "包内 version.json 不合法：version \"bad\" is not YYYY.MM.DD-N",
		},
		{
			name: "missing argument keeps the placeholder",
			id:   AppManifestTooLarge,
			args: []any{"version.json"},
			want: "version.json 超过 {1} 字节",
		},
		{
			name: "extra argument is ignored",
			id:   AppRedirectLimit,
			args: []any{5, 6},
			want: "超过 5 次重定向",
		},
		{
			name: "braces in an argument are not expanded again",
			id:   AppClientFileUnreadable,
			args: []any{"{0}"},
			want: "无法读取 appUrl 指向的文件：{0}",
		},
		{
			name: "unknown id is returned as is",
			id:   "no.such.text_id",
			args: []any{1},
			want: "no.such.text_id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Format(tc.id, tc.args...); got != tc.want {
				t.Fatalf("Format(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

func TestGet(t *testing.T) {
	if got, want := Get(AppDecompressFailed), "解压更新包失败"; got != want {
		t.Fatalf("Get(%q) = %q, want %q", AppDecompressFailed, got, want)
	}
	if got := Get("no.such.id"); got != "no.such.id" {
		t.Fatalf("Get of an unknown id = %q, want the id itself", got)
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		want    map[string]string
		wantErr string
	}{
		{name: "empty file", src: "", want: map[string]string{}},
		{
			name: "comments and blank lines",
			src:  "# 注释\n\n   \n  # 缩进的注释\napi.text.one = 值\n",
			want: map[string]string{"api.text.one": "值"},
		},
		{
			name: "first = splits the line",
			src:  "api.text.one = a = b\n",
			want: map[string]string{"api.text.one": "a = b"},
		},
		{
			name: "key and value are trimmed, inner spaces kept",
			src:  "  api.text.one   =  值  值  \n",
			want: map[string]string{"api.text.one": "值  值"},
		},
		{
			name: "log class is allowed",
			src:  "api.log.one = 值\n",
			want: map[string]string{"api.log.one": "值"},
		},
		{name: "key without =", src: "api.text.one\n", wantErr: "test.lang:1: line has no ="},
		{name: "empty value", src: "api.text.one =\n", wantErr: "test.lang:1: api.text.one has an empty value"},
		{name: "duplicate key", src: "api.text.one = a\napi.text.one = b\n", wantErr: "test.lang:2: duplicate key api.text.one"},
		{name: "uppercase in key", src: "api.text.One = 值\n", wantErr: "outside [a-z0-9_.]"},
		{name: "dash in key", src: "api.text.one-two = 值\n", wantErr: "outside [a-z0-9_.]"},
		{name: "two segments", src: "api.text = 值\n", wantErr: "<package>.<class>.<name>"},
		{name: "empty segment", src: "api..one = 值\n", wantErr: "<package>.<class>.<name>"},
		{name: "unknown class", src: "api.txt.one = 值\n", wantErr: "has class \"txt\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parse("test.lang", tc.src)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parse succeeded, want an error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parse error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parse = %v, want %v", got, tc.want)
			}
		})
	}
}

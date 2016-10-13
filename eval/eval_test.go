package eval

import (
	"errors"
	"os"
	"reflect"
	"strconv"
	"syscall"
	"testing"

	"github.com/elves/elvish/parse"
	"github.com/elves/elvish/util"
)

func TestBuiltinNamespace(t *testing.T) {
	pid := strconv.Itoa(syscall.Getpid())
	if ToString(builtinNamespace["pid"].Get()) != pid {
		t.Errorf(`ev.builtin["pid"] = %v, want %v`, builtinNamespace["pid"], pid)
	}
}

var errAny = errors.New("")

type more struct {
	wantBytesOut []byte
	wantError    error
	wantFalse    bool
}

var noout = []Value{}
var nomore more

var evalTests = []struct {
	text    string
	wantOut []Value
	more
}{
	// Chunks.
	// Empty chunk
	{"", []Value{}, nomore},
	// Outputs of pipelines in a chunk are concatenated
	{"put x; put y; put z", strs("x", "y", "z"), nomore},
	// A failed pipeline cause the whole chunk to fail
	{"put a; e:false; put b", strs("a"), more{wantError: errAny}},

	// Pipelines.
	// Pure byte pipeline
	{`echo "Albert\nAllan\nAlbraham\nBerlin" | sed s/l/1/g | grep e`,
		[]Value{}, more{wantBytesOut: []byte("A1bert\nBer1in\n")}},
	// Pure channel pipeline
	{`put 233 42 19 | each [x]{+ $x 10}`, strs("243", "52", "29"), nomore},
	// TODO: Add a useful hybrid pipeline sample

	// List element assignment
	{"li=[foo bar]; li[0]=233; put $@li", strs("233", "bar"), nomore},
	// Map element assignment
	{"di=[&k=v]; di[k]=lorem; di[k2]=ipsum; put $di[k] $di[k2]",
		strs("lorem", "ipsum"), nomore},
	{"d=[&a=[&b=v]]; put $d[a][b]; d[a][b]=u; put $d[a][b]",
		strs("v", "u"), nomore},
	// Multi-assignments.
	{"{a,b}=`put a b`; put $a $b", strs("a", "b"), nomore},
	{"@a=`put a b`; put $@a", strs("a", "b"), nomore},
	{"{a,@b}=`put a b c`; put $@b", strs("b", "c"), nomore},
	// {"di=[&]; di[a b]=`put a b`; put $di[a] $di[b]", strs("a", "b"), nomore},
	// Temporary assignment.
	{"a=alice b=bob; {a,@b}=(put amy ben) put $a $@b; put $a $b",
		strs("amy", "ben", "alice", "bob"), nomore},

	// Control structures.
	// if
	{"if true; then put then; fi", strs("then"), nomore},
	{"if false; then put then; else put else; fi", strs("else"), nomore},
	{"if false; then put 1; elif false; then put 2; else put 3; fi",
		strs("3"), nomore},
	{"if false; then put 2; elif true; then put 2; else put 3; fi",
		strs("2"), nomore},
	// try
	{"try true; except; put bad; else; put good; tried", strs("good"), nomore},
	{"try e:false; except; put bad; else; put good; tried", strs("bad"), nomore},
	// while
	{"x=0; while < $x 4; do put $x; x=(+ $x 1); done",
		strs("0", "1", "2", "3"), nomore},
	// for
	{"for x in tempora mores; do put 'O '$x; done",
		strs("O tempora", "O mores"), nomore},
	// break
	{"for x in a; do break; else put $x; done", noout, nomore},
	// else
	{"for x in a; do put $x; else put $x; done", strs("a"), nomore},
	// continue
	{"for x in a b; do put $x; continue; put $x; done", strs("a", "b"), nomore},
	// begin/end
	{"begin; put lorem; put ipsum; end", strs("lorem", "ipsum"), nomore},

	// Predicates.
	{"false", noout, more{wantFalse: true}},
	{"true | false | true", noout, more{wantFalse: true}},

	// Redirections.
	{"f=`mktemp elvXXXXXX`; echo 233 > $f; cat < $f; rm $f", noout,
		more{wantBytesOut: []byte("233\n")}},
	// Redirections from File object.
	{`fname=(mktemp elvXXXXXX); echo haha > $fname;
	f=(fopen $fname); cat <$f; fclose $f; rm $fname`, noout,
		more{wantBytesOut: []byte("haha\n")}},
	// Redirections from Pipe object.
	{`p=(pipe); echo haha > $p; pwclose $p; cat < $p; prclose $p`, noout,
		more{wantBytesOut: []byte("haha\n")}},

	// Compounding.
	{"put {fi,elvi}sh{1.0,1.1}",
		strs("fish1.0", "fish1.1", "elvish1.0", "elvish1.1"), nomore},

	// List, map and indexing
	{"echo [a b c] [&key=value] | each put",
		strs("[a b c] [&key=value]"), nomore},
	{"put [a b c][2]", strs("c"), nomore},
	{"put [;a;b c][2][0]", strs("b"), nomore},
	{"put [&key=value][key]", strs("value"), nomore},

	// String literal
	{`put 'such \"''literal'`, strs(`such \"'literal`), nomore},
	{`put "much \n\033[31;1m$cool\033[m"`,
		strs("much \n\033[31;1m$cool\033[m"), nomore},

	// Output capture
	{"put (put lorem ipsum)", strs("lorem", "ipsum"), nomore},

	// Boolean capture
	{"put ?(true) ?(false)",
		[]Value{Bool(true), Bool(false)}, nomore},

	// Variable and compounding
	{"x='SHELL'\nput 'WOW, SUCH '$x', MUCH COOL'\n",
		strs("WOW, SUCH SHELL, MUCH COOL"), nomore},
	// Splicing
	{"x=[elvish rules]; put $@x", strs("elvish", "rules"), nomore},

	// Wildcard.
	{"put /*", strs(util.FullNames("/")...), nomore},

	// Tilde.
	{"h=$E:HOME; E:HOME=/foo; put ~ ~/src; E:HOME=$h",
		strs("/foo", "/foo/src"), nomore},

	// Closure
	// Basics
	{"[]{ }", noout, nomore},
	{"[x]{put $x} foo", strs("foo"), nomore},
	// Variable capture
	{"x=lorem; []{x=ipsum}; put $x", strs("ipsum"), nomore},
	{"x=lorem; []{ put $x; x=ipsum }; put $x",
		strs("lorem", "ipsum"), nomore},
	// Shadowing
	{"x=ipsum; []{ local:x=lorem; put $x }; put $x",
		strs("lorem", "ipsum"), nomore},
	// Shadowing by argument
	{"x=ipsum; [x]{ put $x; x=BAD } lorem; put $x",
		strs("lorem", "ipsum"), nomore},
	// Closure captures new local variables every time
	{`fn f []{ x=0; put []{x=(+ $x 1)} []{put $x} }
      {inc1,put1}=(f); $put1; $inc1; $put1
	  {inc2,put2}=(f); $put2; $inc2; $put2`,
		strs("0", "1", "0", "1"), nomore},
	// Positional variables.
	{`{ put $1 } lorem ipsum`, strs("ipsum"), nomore},
	// Positional variables in the up: namespace.
	{`{ { put $up:0 } in } out`, strs("out"), nomore},

	// fn.
	{"fn f [x]{ put x=$x'.' }; f lorem; f ipsum",
		strs("x=lorem.", "x=ipsum."), nomore},
	// return.
	{"fn f []{ put a; return; put b }; f", strs("a"), nomore},

	// rest args and $args.
	{"[x @xs]{ put $x $xs $args } a b c",
		[]Value{String("a"),
			NewList(String("b"), String("c")),
			NewList(String("a"), String("b"), String("c"))}, nomore},
	// $args.
	{"{ put $args } lorem ipsum",
		[]Value{NewList(String("lorem"), String("ipsum"))}, nomore},

	// Namespaces
	// Pseudo-namespaces local: and up:
	{"x=lorem; []{local:x=ipsum; put $up:x $local:x}",
		strs("lorem", "ipsum"), nomore},
	{"x=lorem; []{up:x=ipsum; put $x}; put $x",
		strs("ipsum", "ipsum"), nomore},
	// Pseudo-namespace E:
	{"E:FOO=lorem; put $E:FOO", strs("lorem"), nomore},
	{"del E:FOO; put $E:FOO", strs(""), nomore},
	// TODO: Test module namespace

	// Builtin functions
	// -----------------

	{`true`, noout, nomore},
	{`false`, noout, more{wantFalse: true}},

	{"kind-of bare 'str' [] [&] []{ }",
		strs("string", "string", "list", "map", "fn"), nomore},

	{`put foo bar`, strs("foo", "bar"), nomore},
	{`unpack [foo bar]`, strs("foo", "bar"), nomore},

	{`print [foo bar]`, noout, more{wantBytesOut: []byte("[foo bar]")}},
	{`echo [foo bar]`, noout, more{wantBytesOut: []byte("[foo bar]\n")}},
	{`pprint [foo bar]`, noout, more{wantBytesOut: []byte("[\n foo\n bar\n]\n")}},

	{`print "a\nb" | slurp`, strs("a\nb"), nomore},
	{`print "a\nb" | from-lines`, strs("a", "b"), nomore},
	{`print "a\nb\n" | from-lines`, strs("a", "b"), nomore},
	{`echo '{"k": "v", "a": [1, 2]}' '"foo"' | from-json`, []Value{
		NewMap(map[Value]Value{
			String("k"): String("v"),
			String("a"): NewList(strs("1", "2")...)}),
		String("foo"),
	}, nomore},

	{`put "l\norem" ipsum | to-lines`, noout,
		more{wantBytesOut: []byte("l\norem\nipsum\n")}},
	{`put [&k=v &a=[1 2]] foo | to-json`, noout,
		more{wantBytesOut: []byte(`{"a":["1","2"],"k":"v"}
"foo"
`)}},

	{`joins : [/usr /bin /tmp]`, strs("/usr:/bin:/tmp"), nomore},
	{`splits &sep=: /usr:/bin:/tmp`, strs("/usr", "/bin", "/tmp"), nomore},

	{`==s haha haha`, noout, nomore},
	{`==s 10 10.0`, noout, more{wantFalse: true}},
	{`<s a b`, noout, nomore},
	{`<s 2 10`, noout, more{wantFalse: true}},

	{`fail haha`, noout, more{wantError: errAny}},
	{`return`, noout, more{wantError: Return}},

	{`f=(constantly foo); $f; $f`, strs("foo", "foo"), nomore},
	{`put 1 233 | each put`, strs("1", "233"), nomore},
	{`echo "1\n233" | each put`, strs("1", "233"), nomore},
	{`each put [1 233]`, strs("1", "233"), nomore},
	// TODO: test peach

	{`range 100 | take 2`, strs("0", "1"), nomore},
	{`range 100 | count`, strs("100"), nomore},
	{`count [(range 100)]`, strs("100"), nomore},

	{`echo "  ax  by cz  \n11\t22 33" | eawk { put $args[-1] }`,
		strs("cz", "33"), nomore},

	{`path-base a/b/c.png`, strs("c.png"), nomore},

	// TODO test more edge cases
	{"+ 233100 233", strs("233333"), nomore},
	{"- 233333 233100", strs("233"), nomore},
	{"* 353 661", strs("233333"), nomore},
	{"/ 233333 353", strs("661"), nomore},
	{"/ 1 0", strs("+Inf"), nomore},
	{"^ 16 2", strs("256"), nomore},
	{"% 23 7", strs("2"), nomore},

	{`== 1 1.0`, noout, nomore},
	{`== 10 0xa`, noout, nomore},
	{`== a a`, noout, more{wantError: errAny}},
	{`> 0x10 1`, noout, nomore},

	{`is 1 1`, noout, nomore},
	{`is [] []`, noout, more{wantFalse: true}},
	{`eq 1 1`, noout, nomore},
	{`eq [] []`, noout, nomore},

	{`ord a`, strs("0x61"), nomore},
	{`base 16 42 233`, strs("2a", "e9"), nomore},
	{`wcswidth 你好`, strs("4"), nomore},
}

func strs(ss ...string) []Value {
	vs := make([]Value, len(ss))
	for i, s := range ss {
		vs[i] = String(s)
	}
	return vs
}

func bools(bs ...bool) []Value {
	vs := make([]Value, len(bs))
	for i, b := range bs {
		vs[i] = Bool(b)
	}
	return vs
}

func mustParseAndCompile(t *testing.T, ev *Evaler, name, text string) Op {
	n, err := parse.Parse(name, text)
	if err != nil {
		t.Fatalf("Parse(%q) error: %s", text, err)
	}
	op, err := ev.Compile(n, name, text)
	if err != nil {
		t.Fatalf("Compile(Parse(%q)) error: %s", text, err)
	}
	return op
}

func evalAndCollect(t *testing.T, texts []string, chsize int) ([]Value, []byte, bool, error) {
	name := "<eval test>"
	ev := NewEvaler(nil)

	// Collect byte output
	outBytes := []byte{}
	pr, pw, _ := os.Pipe()
	bytesDone := make(chan struct{})
	go func() {
		for {
			var buf [64]byte
			nr, err := pr.Read(buf[:])
			outBytes = append(outBytes, buf[:nr]...)
			if err != nil {
				break
			}
		}
		close(bytesDone)
	}()

	// Channel output
	outs := []Value{}

	// Boolean return and error. Only those of the last text is saved.
	var ret bool
	var ex error

	for _, text := range texts {
		op := mustParseAndCompile(t, ev, name, text)

		outCh := make(chan Value, chsize)
		outDone := make(chan struct{})
		go func() {
			for v := range outCh {
				outs = append(outs, v)
			}
			close(outDone)
		}()

		ports := []*Port{
			{File: os.Stdin},
			{File: pw, Chan: outCh},
			{File: os.Stderr},
		}

		ret, ex = ev.eval(op, ports, name, text)
		close(outCh)
		<-outDone
	}

	pw.Close()
	<-bytesDone
	pr.Close()

	return outs, outBytes, ret, ex
}

func TestEval(t *testing.T) {
	for _, tt := range evalTests {
		// fmt.Printf("eval %q\n", tt.text)

		out, bytesOut, ret, err := evalAndCollect(t, []string{tt.text}, len(tt.wantOut))

		good := true
		errorf := func(format string, args ...interface{}) {
			if good {
				good = false
				t.Errorf("eval(%q) fails:", tt.text)
			}
			t.Errorf(format, args...)
		}

		if !reflect.DeepEqual(tt.wantOut, out) {
			errorf("got out=%v, want %v", out, tt.wantOut)
		}
		if string(bytesOut) != string(tt.wantBytesOut) {
			errorf("got bytesOut=%q, want %q", bytesOut, tt.wantBytesOut)
		}
		if ret != !tt.wantFalse {
			errorf("got ret=%v, want %v", ret, !tt.wantFalse)
		}
		// Check error. We accept errAny as a "wildcard" for all non-nil errors.
		if !(tt.wantError == errAny && err != nil) && !reflect.DeepEqual(tt.wantError, err) {
			errorf("got err=%v, want %v", err, tt.wantError)
		}
		if !good {
			t.Errorf("--------------")
		}
	}
}

func TestMultipleEval(t *testing.T) {
	texts := []string{"x=hello", "put $x"}
	outs, _, _, err := evalAndCollect(t, texts, 1)
	wanted := strs("hello")
	if err != nil {
		t.Errorf("eval %s => %v, want nil", texts, err)
	}
	if !reflect.DeepEqual(outs, wanted) {
		t.Errorf("eval %s outputs %v, want %v", texts, outs, wanted)
	}
}

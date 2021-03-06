package eval

// Builtin special forms. Special forms behave mostly like ordinary commands -
// they are valid commands syntactically, and can take part in pipelines - but
// they have special rules for the evaluation of their arguments and can affect
// the compilation phase (whereas ordinary commands can only affect the
// evaluation phase).
//
// For instance, the "and" special form evaluates its arguments from left to
// right, and stops as soon as one booleanly false value is obtained: the
// command "and $false (fail haha)" does not produce an exception.
//
// As another instance, the "del" special form removes a variable, affecting the
// compiler.
//
// Flow control structures are also implemented as special forms in elvish, with
// closures functioning as code blocks.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/elves/elvish/eval/types"
	"github.com/elves/elvish/eval/vartypes"
	"github.com/elves/elvish/parse"
)

type compileBuiltin func(*compiler, *parse.Form) OpBody

var (
	// ErrNoLibDir is thrown by "use" when the Evaler does not have a library
	// directory.
	ErrNoLibDir = errors.New("Evaler does not have a lib directory")
	// ErrRelativeUseNotFromMod is thrown by "use" when relative use is used
	// not from a module
	ErrRelativeUseNotFromMod = errors.New("Relative use not from module")
	// ErrRelativeUseGoesOutsideLib is thrown when a relative use goes out of
	// the library directory.
	ErrRelativeUseGoesOutsideLib = errors.New("Module outside library directory")
)

var builtinSpecials map[string]compileBuiltin

// IsBuiltinSpecial is the set of all names of builtin special forms. It is
// intended for external consumption, e.g. the syntax highlighter.
var IsBuiltinSpecial = map[string]bool{}

func init() {
	// Needed to avoid initialization loop
	builtinSpecials = map[string]compileBuiltin{
		"del":   compileDel,
		"fn":    compileFn,
		"use":   compileUse,
		"and":   compileAnd,
		"or":    compileOr,
		"if":    compileIf,
		"while": compileWhile,
		"for":   compileFor,
		"try":   compileTry,
	}
	for name := range builtinSpecials {
		IsBuiltinSpecial[name] = true
	}
}

const delArgMsg = "arguments to del must be variable or variable elements"

// DelForm = 'del' { VariablePrimary }
func compileDel(cp *compiler, fn *parse.Form) OpBody {
	var ops []Op
	for _, cn := range fn.Args {
		cp.compiling(cn)
		if len(cn.Indexings) != 1 {
			cp.errorf(delArgMsg)
			continue
		}
		head, indicies := cn.Indexings[0].Head, cn.Indexings[0].Indicies
		if head.Type != parse.Bareword {
			if head.Type == parse.Variable {
				cp.errorf("arguments to del must drop $")
			} else {
				cp.errorf(delArgMsg)
			}
			continue
		}

		explode, ns, name := ParseVariable(head.Value)
		if explode {
			cp.errorf("arguments to del may be have a leading @")
			continue
		}
		var f OpBody
		if len(indicies) == 0 {
			switch ns {
			case "", "local":
				if !cp.thisScope().has(name) {
					cp.errorf("no variable $%s in local scope", name)
					continue
				}
				cp.thisScope().del(name)
				f = delLocalVarOp{name}
			case "E":
				f = delEnvVarOp{name}
			default:
				cp.errorf("only variables in local: or E: can be deleted")
				continue
			}
		} else {
			if !cp.registerVariableGet(ns, name) {
				cp.errorf("no variable $%s", head.Value)
				continue
			}
			f = newDelElementOp(ns, name, head.Begin(), head.End(), cp.arrayOps(indicies))
		}
		ops = append(ops, Op{f, cn.Begin(), cn.End()})
	}
	return seqOp{ops}
}

type delLocalVarOp struct{ name string }

func (op delLocalVarOp) Invoke(fm *Frame) error {
	delete(fm.local, op.name)
	return nil
}

type delEnvVarOp struct{ name string }

func (op delEnvVarOp) Invoke(*Frame) error {
	return os.Unsetenv(op.name)
}

func newDelElementOp(ns, name string, begin, headEnd int, indexOps []ValuesOp) OpBody {
	ends := make([]int, len(indexOps)+1)
	ends[0] = headEnd
	for i, op := range indexOps {
		ends[i+1] = op.End
	}
	return &delElemOp{ns, name, indexOps, begin, ends}
}

type delElemOp struct {
	ns       string
	name     string
	indexOps []ValuesOp
	begin    int
	ends     []int
}

func (op *delElemOp) Invoke(fm *Frame) error {
	var indicies []types.Value
	for _, indexOp := range op.indexOps {
		indexValues, err := indexOp.Exec(fm)
		if err != nil {
			return err
		}
		if len(indexValues) != 1 {
			fm.errorpf(indexOp.Begin, indexOp.End, "index must evaluate to a single value in argument to del")
		}
		indicies = append(indicies, indexValues[0])
	}
	err := vartypes.DelElement(fm.ResolveVar(op.ns, op.name), indicies)
	if err != nil {
		if level := vartypes.GetElementErrorLevel(err); level >= 0 {
			fm.errorpf(op.begin, op.ends[level], "%s", err.Error())
		}
		return err
	}
	return nil
}

// FnForm = 'fn' StringPrimary LambdaPrimary
//
// fn f []{foobar} is a shorthand for set '&'f = []{foobar}.
func compileFn(cp *compiler, fn *parse.Form) OpBody {
	args := cp.walkArgs(fn)
	nameNode := args.next()
	varName := mustString(cp, nameNode, "must be a literal string") + FnSuffix
	bodyNode := args.nextMustLambda()
	args.mustEnd()

	cp.registerVariableSetQname(":" + varName)
	op := cp.lambda(bodyNode)

	return fnOp{varName, op}
}

type fnOp struct {
	varName  string
	lambdaOp ValuesOpBody
}

func (op fnOp) Invoke(fm *Frame) error {
	// Initialize the function variable with the builtin nop function. This step
	// allows the definition of recursive functions; the actual function will
	// never be called.
	fm.local[op.varName] = vartypes.NewPtr(&BuiltinFn{"<shouldn't be called>", nop})
	values, err := op.lambdaOp.Invoke(fm)
	if err != nil {
		return err
	}
	closure := values[0].(*Closure)
	closure.Op = wrapFn(closure.Op)
	return fm.local[op.varName].Set(closure)
}

func wrapFn(op Op) Op {
	return Op{fnWrap{op}, op.Begin, op.End}
}

type fnWrap struct{ wrapped Op }

func (op fnWrap) Invoke(ec *Frame) error {
	err := ec.PEval(op.wrapped)
	if err != nil && err.(*Exception).Cause != Return {
		// rethrow
		return err
	}
	return nil
}

// UseForm = 'use' StringPrimary
func compileUse(cp *compiler, fn *parse.Form) OpBody {
	if len(fn.Args) == 0 {
		end := fn.Head.End()
		cp.errorpf(end, end, "lack module name")
	} else if len(fn.Args) >= 2 {
		cp.errorpf(fn.Args[1].Begin(), fn.Args[len(fn.Args)-1].End(), "superfluous argument(s)")
	}

	spec := mustString(cp, fn.Args[0], "should be a literal string")

	// When modspec = "a/b/c:d", modname is c:d, and modpath is a/b/c/d
	modname := spec[strings.LastIndexByte(spec, '/')+1:]
	modpath := strings.Replace(spec, ":", "/", -1)
	cp.thisScope().set(modname + NsSuffix)

	return useOp{modname, modpath}
}

type useOp struct{ modname, modpath string }

func (op useOp) Invoke(fm *Frame) error {
	return use(fm, op.modname, op.modpath)
}

func use(ec *Frame, modname, modpath string) error {
	resolvedPath := ""
	if strings.HasPrefix(modpath, "./") || strings.HasPrefix(modpath, "../") {
		if ec.srcMeta.typ != SrcModule {
			return ErrRelativeUseNotFromMod
		}
		// Resolve relative modpath.
		resolvedPath = filepath.Clean(filepath.Dir(ec.srcMeta.name) + "/" + modpath)
	} else {
		resolvedPath = filepath.Clean(modpath)
	}
	if strings.HasPrefix(resolvedPath, "../") {
		return ErrRelativeUseGoesOutsideLib
	}

	// Put the just loaded module into local scope.
	ns, err := loadModule(ec, resolvedPath)
	if err != nil {
		return err
	}
	ec.local[modname+NsSuffix] = vartypes.NewPtr(ns)
	return nil
}

func loadModule(ec *Frame, name string) (Ns, error) {
	if ns, ok := ec.Evaler.modules[name]; ok {
		// Module already loaded.
		return ns, nil
	}

	// Load the source.
	var path, code string

	if ec.libDir == "" {
		return nil, ErrNoLibDir
	}

	path = filepath.Join(ec.libDir, name+".elv")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// File does not exist. Try loading from the table of builtin
		// modules.
		var ok bool
		if code, ok = ec.bundled[name]; ok {
			// Source is loaded. Do nothing more.
			path = "<builtin module>"
		} else {
			return nil, fmt.Errorf("cannot load %s: %s does not exist", name, path)
		}
	} else {
		// File exists. Load it.
		code, err = readFileUTF8(path)
		if err != nil {
			return nil, err
		}
	}

	n, err := parse.Parse(name, code)
	if err != nil {
		return nil, err
	}

	// Make an empty scope to evaluate the module in.
	meta := NewModuleSource(name, path, code)
	modGlobal := Ns{}

	newEc := &Frame{
		ec.Evaler, meta,
		modGlobal, make(Ns),
		ec.ports,
		0, len(code), ec.addTraceback(), false,
	}

	op, err := newEc.Compile(n, meta)
	if err != nil {
		return nil, err
	}

	// Load the namespace before executing. This avoids mutual and self use's to
	// result in an infinite recursion.
	ec.Evaler.modules[name] = modGlobal
	err = newEc.PEval(op)
	if err != nil {
		// Unload the namespace.
		delete(ec.modules, name)
		return nil, err
	}
	return modGlobal, nil
}

// compileAnd compiles the "and" special form.
//
// The and special form evaluates arguments until a false-ish values is found
// and outputs it; the remaining arguments are not evaluated. If there are no
// false-ish values, the last value is output. If there are no arguments, it
// outputs $true, as if there is a hidden $true before actual arguments.
func compileAnd(cp *compiler, fn *parse.Form) OpBody {
	return &andOrOp{cp.compoundOps(fn.Args), true, false}
}

// compileOr compiles the "or" special form.
//
// The or special form evaluates arguments until a true-ish values is found and
// outputs it; the remaining arguments are not evaluated. If there are no
// true-ish values, the last value is output. If there are no arguments, it
// outputs $false, as if there is a hidden $false before actual arguments.
func compileOr(cp *compiler, fn *parse.Form) OpBody {
	return &andOrOp{cp.compoundOps(fn.Args), false, true}
}

type andOrOp struct {
	argOps []ValuesOp
	init   bool
	stopAt bool
}

func (op *andOrOp) Invoke(fm *Frame) error {
	var lastValue types.Value = types.Bool(op.init)
	for _, argOp := range op.argOps {
		values, err := argOp.Exec(fm)
		if err != nil {
			return err
		}
		for _, value := range values {
			if types.ToBool(value) == op.stopAt {
				fm.OutputChan() <- value
				return nil
			}
			lastValue = value
		}
	}
	fm.OutputChan() <- lastValue
	return nil
}

func compileIf(cp *compiler, fn *parse.Form) OpBody {
	args := cp.walkArgs(fn)
	var condNodes []*parse.Compound
	var bodyNodes []*parse.Primary
	for {
		condNodes = append(condNodes, args.next())
		bodyNodes = append(bodyNodes, args.nextMustLambda())
		if !args.nextIs("elif") {
			break
		}
	}
	elseNode := args.nextMustLambdaIfAfter("else")
	args.mustEnd()

	condOps := cp.compoundOps(condNodes)
	bodyOps := cp.primaryOps(bodyNodes)
	var elseOp ValuesOp
	if elseNode != nil {
		elseOp = cp.primaryOp(elseNode)
	}

	return &ifOp{condOps, bodyOps, elseOp}
}

type ifOp struct {
	condOps []ValuesOp
	bodyOps []ValuesOp
	elseOp  ValuesOp
}

func (op *ifOp) Invoke(fm *Frame) error {
	bodies := make([]Callable, len(op.bodyOps))
	for i, bodyOp := range op.bodyOps {
		bodies[i] = bodyOp.execlambdaOp(fm)
	}
	else_ := op.elseOp.execlambdaOp(fm)
	for i, condOp := range op.condOps {
		condValues, err := condOp.Exec(fm.fork("if cond"))
		if err != nil {
			return err
		}
		if allTrue(condValues) {
			return bodies[i].Call(fm.fork("if body"), NoArgs, NoOpts)
		}
	}
	if op.elseOp.Body != nil {
		return else_.Call(fm.fork("if else"), NoArgs, NoOpts)
	}
	return nil
}

func compileWhile(cp *compiler, fn *parse.Form) OpBody {
	args := cp.walkArgs(fn)
	condNode := args.next()
	bodyNode := args.nextMustLambda()
	args.mustEnd()

	return &whileOp{cp.compoundOp(condNode), cp.primaryOp(bodyNode)}
}

type whileOp struct {
	condOp, bodyOp ValuesOp
}

func (op *whileOp) Invoke(fm *Frame) error {
	body := op.bodyOp.execlambdaOp(fm)

	for {
		condValues, err := op.condOp.Exec(fm.fork("while cond"))
		if err != nil {
			return err
		}
		if !allTrue(condValues) {
			break
		}
		err = fm.fork("while").PCall(body, NoArgs, NoOpts)
		if err != nil {
			exc := err.(*Exception)
			if exc.Cause == Continue {
				// do nothing
			} else if exc.Cause == Break {
				continue
			} else {
				return nil
			}
		}
	}
	return nil
}

func compileFor(cp *compiler, fn *parse.Form) OpBody {
	args := cp.walkArgs(fn)
	varNode := args.next()
	iterNode := args.next()
	bodyNode := args.nextMustLambda()
	elseNode := args.nextMustLambdaIfAfter("else")
	args.mustEnd()

	varOp, restOp := cp.lvaluesOp(varNode.Indexings[0])
	if restOp.Body != nil {
		cp.errorpf(restOp.Begin, restOp.End, "rest not allowed")
	}

	iterOp := cp.compoundOp(iterNode)
	bodyOp := cp.primaryOp(bodyNode)
	var elseOp ValuesOp
	if elseNode != nil {
		elseOp = cp.primaryOp(elseNode)
	}

	return &forOp{varOp, iterOp, bodyOp, elseOp}
}

type forOp struct {
	varOp  LValuesOp
	iterOp ValuesOp
	bodyOp ValuesOp
	elseOp ValuesOp
}

func (op *forOp) Invoke(ec *Frame) error {
	variables, err := op.varOp.Exec(ec)
	if err != nil {
		return err
	}
	if len(variables) != 1 {
		ec.errorpf(op.varOp.Begin, op.varOp.End, "only one variable allowed")
	}
	variable := variables[0]
	iterable := ec.ExecAndUnwrap("value being iterated", op.iterOp).One().Any()

	body := op.bodyOp.execlambdaOp(ec)
	elseBody := op.elseOp.execlambdaOp(ec)

	iterated := false
	var errElement error
	errIterate := types.Iterate(iterable, func(v types.Value) bool {
		iterated = true
		err := variable.Set(v)
		if err != nil {
			errElement = err
			return false
		}
		err = ec.fork("for").PCall(body, NoArgs, NoOpts)
		if err != nil {
			exc := err.(*Exception)
			if exc.Cause == Continue {
				// do nothing
			} else if exc.Cause == Break {
				return false
			} else {
				errElement = err
				return false
			}
		}
		return true
	})
	if errIterate != nil {
		return errIterate
	}
	if errElement != nil {
		return errElement
	}

	if !iterated && elseBody != nil {
		return elseBody.Call(ec.fork("for else"), NoArgs, NoOpts)
	}
	return nil
}

func compileTry(cp *compiler, fn *parse.Form) OpBody {
	logger.Println("compiling try")
	args := cp.walkArgs(fn)
	bodyNode := args.nextMustLambda()
	logger.Printf("body is %q", bodyNode.SourceText())
	var exceptVarNode *parse.Indexing
	var exceptNode *parse.Primary
	if args.nextIs("except") {
		logger.Println("except-ing")
		n := args.peek()
		// Is this a variable?
		if len(n.Indexings) == 1 && n.Indexings[0].Head.Type == parse.Bareword {
			exceptVarNode = n.Indexings[0]
			args.next()
		}
		exceptNode = args.nextMustLambda()
	}
	elseNode := args.nextMustLambdaIfAfter("else")
	finallyNode := args.nextMustLambdaIfAfter("finally")
	args.mustEnd()

	var exceptVarOp LValuesOp
	var bodyOp, exceptOp, elseOp, finallyOp ValuesOp
	bodyOp = cp.primaryOp(bodyNode)
	if exceptVarNode != nil {
		var restOp LValuesOp
		exceptVarOp, restOp = cp.lvaluesOp(exceptVarNode)
		if restOp.Body != nil {
			cp.errorpf(restOp.Begin, restOp.End, "may not use @rest in except variable")
		}
	}
	if exceptNode != nil {
		exceptOp = cp.primaryOp(exceptNode)
	}
	if elseNode != nil {
		elseOp = cp.primaryOp(elseNode)
	}
	if finallyNode != nil {
		finallyOp = cp.primaryOp(finallyNode)
	}

	return &tryOp{bodyOp, exceptVarOp, exceptOp, elseOp, finallyOp}
}

type tryOp struct {
	bodyOp      ValuesOp
	exceptVarOp LValuesOp
	exceptOp    ValuesOp
	elseOp      ValuesOp
	finallyOp   ValuesOp
}

func (op *tryOp) Invoke(ec *Frame) error {
	body := op.bodyOp.execlambdaOp(ec)
	exceptVar := op.exceptVarOp.execMustOne(ec)
	except := op.exceptOp.execlambdaOp(ec)
	else_ := op.elseOp.execlambdaOp(ec)
	finally := op.finallyOp.execlambdaOp(ec)

	err := ec.fork("try body").PCall(body, NoArgs, NoOpts)
	if err != nil {
		if except != nil {
			if exceptVar != nil {
				err := exceptVar.Set(err.(*Exception))
				if err != nil {
					return err
				}
			}
			err = ec.fork("try except").PCall(except, NoArgs, NoOpts)
		}
	} else {
		if else_ != nil {
			err = ec.fork("try else").PCall(else_, NoArgs, NoOpts)
		}
	}
	if finally != nil {
		return finally.Call(ec.fork("try finally"), NoArgs, NoOpts)
	}
	return err
}

// execLambdaOp executes a ValuesOp that is known to yield a lambda and returns
// the lambda. If the ValuesOp is empty, it returns a nil.
func (op ValuesOp) execlambdaOp(ec *Frame) Callable {
	if op.Body == nil {
		return nil
	}

	values, err := op.Exec(ec)
	if err != nil {
		panic("must not be erroneous")
	}
	return values[0].(Callable)
}

// execMustOne executes the LValuesOp and raises an exception if it does not
// evaluate to exactly one Variable. If the given LValuesOp is empty, it returns
// nil.
func (op LValuesOp) execMustOne(ec *Frame) vartypes.Variable {
	if op.Body == nil {
		return nil
	}
	variables, err := op.Exec(ec)
	maybeThrow(err)
	if len(variables) != 1 {
		ec.errorpf(op.Begin, op.End, "should be one variable")
	}
	return variables[0]
}

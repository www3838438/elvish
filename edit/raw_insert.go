package edit

import (
	"github.com/elves/elvish/edit/ui"
	"github.com/elves/elvish/eval"
	"github.com/elves/elvish/eval/vartypes"
)

// Raw insert mode is a special mode, in that it does not use the normal key
// binding. Rather, insertRaw is called directly from the main loop in
// Editor.ReadLine.

type rawInsert struct {
}

func startInsertRaw(ed *Editor) {
	ed.reader.SetRaw(true)
	ed.mode = rawInsert{}
}

func insertRaw(ed *Editor, r rune) {
	ed.insertAtDot(string(r))
	ed.reader.SetRaw(false)
	ed.mode = &ed.insert
}

func (rawInsert) Binding(map[string]vartypes.Variable, ui.Key) eval.Fn {
	// The raw insert mode does not handle keys.
	return nil
}

func (ri rawInsert) ModeLine() ui.Renderer {
	return modeLineRenderer{" RAW ", ""}
}

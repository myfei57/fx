package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/antonmedv/fx/internal/engine"
	"github.com/antonmedv/fx/internal/format"
	"github.com/antonmedv/fx/internal/jsonx"
)

// newReloadModel creates a model showing file, loaded as main does.
func newReloadModel(t *testing.T, content string) (*model, string) {
	file := filepath.Join(t.TempDir(), "f.json")
	require.NoError(t, os.WriteFile(file, []byte(content), 0o644))
	oldPath := engine.FilePath
	engine.FilePath = file
	t.Cleanup(func() {
		engine.FilePath = oldPath
		engine.SetInput(nil)
	})
	m := newQueryModel(t)
	m.eof = false
	runAll(m, m.reload())
	return m, file
}

// runAll runs cmd, batches included, feeding messages back into Update
// until there are no more commands.
func runAll(m *model, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		cmd, queue = queue[0], queue[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(batch, queue...)
			continue
		}
		_, next := m.Update(msg)
		queue = append([]tea.Cmd{next}, queue...)
	}
}

func reloadKey(m *model) {
	_, cmd := m.Update(press("r"))
	runAll(m, cmd)
}

func TestReload_ShowsNewContent(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1}`)
	require.Equal(t, []string{"{", `"a"1`, "}"}, lines(m))
	require.True(t, m.eof)

	require.NoError(t, os.WriteFile(file, []byte(`{"b": 2}`+"\n"+`[3]`), 0o644))
	reloadKey(m)
	require.Equal(t, []string{"{", `"b"2`, "}", "[", "3", "]"}, lines(m))
	require.True(t, m.eof)
	require.Equal(t, 6, m.totalLines)
}

func TestReload_KeepsCursorPath(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1, "b": {"c": 2}}`)
	m.selectNode(m.findByPath([]any{"b", "c"}))

	require.NoError(t, os.WriteFile(file, []byte(`{"x": 0, "y": 0, "a": 1, "b": {"c": 3}}`), 0o644))
	reloadKey(m)
	at, ok := m.cursorPointsTo()
	require.True(t, ok)
	require.Equal(t, `"c"`, at.Key)
	require.Equal(t, "3", at.Value)
	require.Equal(t, ".b.c", m.cursorPath())
}

func TestReload_KeepsCursorDocument(t *testing.T) {
	m, file := newReloadModel(t, `{"id": 1}`+"\n"+`{"id": 2}`)
	m.selectNode(m.top.End.Next.Next) // .id of the second document

	require.NoError(t, os.WriteFile(file, []byte(`{"id": 10}`+"\n"+`{"id": 20}`), 0o644))
	reloadKey(m)
	at, ok := m.cursorPointsTo()
	require.True(t, ok)
	require.Equal(t, "20", at.Value)
}

func TestReload_MissingPathSelectsDocument(t *testing.T) {
	m, file := newReloadModel(t, `{"a": {"b": 1}}`)
	m.selectNode(m.findByPath([]any{"a", "b"}))

	require.NoError(t, os.WriteFile(file, []byte(`{"z": 1}`), 0o644))
	reloadKey(m)
	at, ok := m.cursorPointsTo()
	require.True(t, ok)
	require.Same(t, m.top, at)
}

func TestReload_KeyUnquoteFails(t *testing.T) {
	m, file := newReloadModel(t, `{"a\/b": 1, "c": 2}`)
	m.selectNode(m.findByPath([]any{"c"}))

	require.NoError(t, os.WriteFile(file, []byte(`{"a\/b": 1, "c": 3}`), 0o644))
	reloadKey(m)
	at, ok := m.cursorPointsTo()
	require.True(t, ok)
	require.Equal(t, "3", at.Value)
}

func TestReload_RunsQueryAgain(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1}`)
	runAll(m, m.doQuery(".a"))
	require.Equal(t, []string{"1"}, lines(m))

	require.NoError(t, os.WriteFile(file, []byte(`{"a": 2}`), 0o644))
	reloadKey(m)
	require.Equal(t, []string{"2"}, lines(m))
	require.NotNil(t, m.original)
	require.True(t, m.query.done)

	runAll(m, m.clearQuery())
	require.Equal(t, []string{"{", `"a"2`, "}"}, lines(m))
}

func TestReload_ErrorKeepsRunning(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1}`)

	require.NoError(t, os.WriteFile(file, []byte(`{"a": `), 0o644))
	reloadKey(m)
	require.Nil(t, m.printErrorOnExit)
	require.True(t, m.eof)
	require.NotEmpty(t, lines(m))
	require.True(t, m.isQueryError(m.top))

	require.NoError(t, os.WriteFile(file, []byte(`{"a": 2}`), 0o644))
	reloadKey(m)
	require.Equal(t, []string{"{", `"a"2`, "}"}, lines(m))
	require.False(t, m.isQueryError(m.top))
}

func TestReload_MissingFile(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1}`)

	require.NoError(t, os.Remove(file))
	reloadKey(m)
	require.Nil(t, m.printErrorOnExit)
	require.True(t, m.eof)
	require.Len(t, lines(m), 1)
	require.True(t, m.isQueryError(m.top))
}

func TestReload_DropsStaleMessages(t *testing.T) {
	m, _ := newReloadModel(t, `{"a": 1}`)
	stale := m.loadGen - 1

	m.Update(nodeMsg{node: parseDoc(t, `[1]`), gen: stale})
	m.Update(errorMsg{err: os.ErrClosed, gen: stale})
	require.Equal(t, []string{"{", `"a"1`, "}"}, lines(m))
	require.Nil(t, m.printErrorOnExit)
}

func TestReload_StdinIsNoop(t *testing.T) {
	oldPath := engine.FilePath
	engine.FilePath = ""
	t.Cleanup(func() { engine.FilePath = oldPath })

	m := newQueryModel(t, `{"a": 1}`)
	reloadKey(m)
	require.Equal(t, []string{"{", `"a"1`, "}"}, lines(m))
	require.Zero(t, m.loadGen)
}

func TestReload_WhileRestoringDoesNotRunQuery(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1}`)
	runAll(m, m.doQuery(".a"))
	m.runningQueries++ // An engine still reads the original: clearQuery waits.
	m.clearQuery()
	require.True(t, m.restoring)

	require.NoError(t, os.WriteFile(file, []byte(`{"a": 2}`), 0o644))
	reloadKey(m)
	require.Nil(t, m.original)
	require.Equal(t, []string{"{", `"a"2`, "}"}, lines(m))
}

func TestReload_KeepsCursorArrayIndexAndRow(t *testing.T) {
	m, file := newReloadModel(t, `{"a": [1, 2, 3]}`)
	m.selectNode(m.findByPath([]any{"a", 2}))
	row := m.cursor

	require.NoError(t, os.WriteFile(file, []byte(`{"a": [1, 2, 30]}`), 0o644))
	reloadKey(m)
	at, _ := m.cursorPointsTo()
	require.Equal(t, "30", at.Value)
	require.Equal(t, row, m.cursor)
}

func TestReload_KeepsCursorInCollapsedMode(t *testing.T) {
	m, file := newReloadModel(t, `{"a": {"b": 1}}`)
	m.collapsed = true
	m.selectNode(m.findByPath([]any{"a", "b"}))

	require.NoError(t, os.WriteFile(file, []byte(`{"a": {"b": 2}}`), 0o644))
	reloadKey(m)
	at, _ := m.cursorPointsTo()
	require.Equal(t, "2", at.Value)
}

func TestReload_CursorOnClosingBracket(t *testing.T) {
	m, file := newReloadModel(t, `{"a": {"b": 1}}`)
	m.selectNode(m.findByPath([]any{"a"}).End)

	require.NoError(t, os.WriteFile(file, []byte(`{"a": {"b": 2}}`), 0o644))
	reloadKey(m)
	at, _ := m.cursorPointsTo()
	require.Equal(t, `"a"`, at.Key)
}

func TestReload_CursorOnWrappedString(t *testing.T) {
	long := `"` + strings.Repeat("word ", 40) + `"`
	m, file := newReloadModel(t, `{"a": 1, "s": `+long+`}`)
	m.wrap = true
	runAll(m, m.reload())
	s := m.findByPath([]any{"s"})
	require.NotNil(t, s.ChunkEnd)
	m.selectNode(s.ChunkEnd) // A wrapped chunk of .s

	require.NoError(t, os.WriteFile(file, []byte(`{"s": `+long+`}`), 0o644))
	reloadKey(m)
	require.Equal(t, ".s", m.cursorPath())
}

func TestReload_KeyPressCancelsPendingRestore(t *testing.T) {
	m, file := newReloadModel(t, `{"id": 1}`+"\n"+`{"id": 2}`)
	m.selectNode(m.top.End.Next.Next)

	require.NoError(t, os.WriteFile(file, []byte(`{"id": 10}`+"\n"+`{"id": 20}`), 0o644))
	_, cmd := m.Update(press("r"))
	msg := cmd().(tea.BatchMsg)[0]() // First document only.
	_, cmd = m.Update(msg)
	m.Update(press("down"))
	runAll(m, cmd)
	at, _ := m.cursorPointsTo()
	require.Equal(t, `"id"`, at.Key)
	require.Equal(t, "10", at.Value)
}

func TestReload_Yaml(t *testing.T) {
	inputFormat = format.YAML
	t.Cleanup(func() { inputFormat = nil })
	m, file := newReloadModel(t, "a: 1\n")
	require.Equal(t, []string{"{", `"a"1`, "}"}, lines(m))

	require.NoError(t, os.WriteFile(file, []byte("a: 2\n"), 0o644))
	reloadKey(m)
	require.Equal(t, []string{"{", `"a"2`, "}"}, lines(m))
}

// A strict parse error must not look like the end of input to a query:
// save() would overwrite the file with the part read before the error.
func TestReload_StrictErrorDoesNotSave(t *testing.T) {
	flagStrict = true
	t.Cleanup(func() { flagStrict = false })
	m, file := newReloadModel(t, `{"a": 1}`)
	runAll(m, m.doQuery(`save({a: x.a + 1})`))
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.JSONEq(t, `{"a": 2}`, string(data))

	broken := `{"a": 5} oops`
	require.NoError(t, os.WriteFile(file, []byte(broken), 0o644))
	reloadKey(m)
	data, err = os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, broken, string(data))
}

// The file of a stopped loader must not be closed while its parser may
// still read it.
func TestReload_RapidReloads(t *testing.T) {
	var content strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&content, "{\"i\": %d}\n", i)
	}
	m, _ := newReloadModel(t, content.String())
	for range 50 {
		m.reload()
	}
	runAll(m, m.reload())
	require.True(t, m.eof)
	require.Equal(t, 20000*3, m.totalLines)
}

func TestReload_DoesNotFollowBottom(t *testing.T) {
	m, file := newReloadModel(t, "1\n2\n3")
	m.selectNode(m.top)

	require.NoError(t, os.WriteFile(file, []byte("1\n2\n3"), 0o644))
	reloadKey(m)
	at, _ := m.cursorPointsTo()
	require.Same(t, m.top, at)
}

func TestReload_KeepsQueryAcrossOpenFailure(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1}`)
	runAll(m, m.doQuery(".a"))

	require.NoError(t, os.Remove(file))
	reloadKey(m)
	require.True(t, m.isQueryError(m.top))

	require.NoError(t, os.WriteFile(file, []byte(`{"a": 2}`), 0o644))
	reloadKey(m)
	require.Equal(t, []string{"2"}, lines(m))
}

type blockingParser struct {
	engine.Parser
	entered, release chan struct{}
	closed           *atomic.Bool
	closedInParse    atomic.Bool
}

func (p *blockingParser) Parse() (*jsonx.Node, error) {
	close(p.entered)
	<-p.release
	p.closedInParse.Store(p.closed.Load())
	return nil, io.EOF
}

type closer struct{ closed *atomic.Bool }

func (c closer) Close() error { c.closed.Store(true); return nil }

// A stopped loader closes its file only once the parser is done with it:
// a read of a closed file panics in the parser (outside Parse's recover).
func TestLoader_ClosesFileAfterParser(t *testing.T) {
	var closed atomic.Bool
	p := &blockingParser{entered: make(chan struct{}), release: make(chan struct{}), closed: &closed}
	l := newLoader(1, closer{&closed}, func() (engine.Parser, error) { return p, nil })
	<-p.entered
	l.stopLoading()
	require.False(t, closed.Load())
	close(p.release)
	require.Eventually(t, closed.Load, time.Second, time.Millisecond)
	require.False(t, p.closedInParse.Load())
}

type failingParser struct{ engine.Parser }

func (failingParser) Parse() (*jsonx.Node, error) { return nil, os.ErrClosed }
func (failingParser) Recover() *jsonx.Node        { return nil }

// A parser that can't recover (LineParser) ends with its error.
func TestLoader_ParserWithoutRecover(t *testing.T) {
	l := newLoader(0, nil, func() (engine.Parser, error) { return failingParser{}, nil })
	msg := l.wait()()
	require.Equal(t, errorMsg{err: os.ErrClosed}, msg)
}

func TestReload_SummarySuccess(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1}`)

	require.NoError(t, os.WriteFile(file, []byte(`{"a": 1}`+"\n"+`{"b": 2}`+"\n"+`[3]`), 0o644))
	reloadKey(m)

	require.True(t, m.eof)
	require.Equal(t, m.loadGen, m.loadSummary.gen)
	require.Equal(t, 3, m.loadSummary.docs)
	require.False(t, m.loadSummary.cancelled)
	require.Zero(t, m.loadSummary.errLine)
	require.NotNil(t, m.message)
	require.False(t, m.message.isErr)
	require.Equal(t, "reloaded: 3 documents", m.message.text)
}

// A strict parse error ends the reload with the documents parsed before
// it and the line of the first error.
func TestReload_SummaryStrictError(t *testing.T) {
	flagStrict = true
	t.Cleanup(func() { flagStrict = false })
	m, file := newReloadModel(t, `{"a": 1}`)

	require.NoError(t, os.WriteFile(file, []byte(`{"a": 1}`+"\n"+`{"b": `), 0o644))
	reloadKey(m)

	require.Equal(t, m.loadGen, m.loadSummary.gen)
	require.Equal(t, 1, m.loadSummary.docs, "the document before the error is counted")
	require.False(t, m.loadSummary.cancelled)
	require.Equal(t, 2, m.loadSummary.errLine)
	require.NotNil(t, m.message)
	require.True(t, m.message.isErr)
	require.Contains(t, m.message.text, "line 2")
	require.NotContains(t, m.message.text, "{", "the summary keeps no input content")
}

// An error without a location (a missing file) reports the failure with
// no error line.
func TestReload_SummaryMissingFile(t *testing.T) {
	m, _ := newReloadModel(t, `{"a": 1}`)
	// A path that never existed: removing the open file races its close
	// on Windows, so point the reload at a missing path instead.
	engine.FilePath = filepath.Join(t.TempDir(), "missing.json")

	reloadKey(m)

	require.True(t, m.eof)
	require.Equal(t, m.loadGen, m.loadSummary.gen)
	require.Zero(t, m.loadSummary.docs)
	require.False(t, m.loadSummary.cancelled)
	require.Zero(t, m.loadSummary.errLine)
	require.NotNil(t, m.message)
	require.True(t, m.message.isErr)
	require.Equal(t, "reload failed: 0 documents parsed", m.message.text)
}

// Many reloads in a row: the summary is that of the last load only.
func TestReload_SummaryConsecutiveReloads(t *testing.T) {
	var content strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&content, "{\"i\": %d}\n", i)
	}
	m, _ := newReloadModel(t, content.String())
	for range 20 {
		m.reload()
	}
	runAll(m, m.reload())

	require.True(t, m.eof)
	require.Equal(t, m.loadGen, m.loadSummary.gen)
	require.Equal(t, 2000, m.loadSummary.docs)
	require.False(t, m.loadSummary.cancelled)
	require.Zero(t, m.loadSummary.errLine)
	require.Equal(t, "reloaded: 2000 documents", m.message.text)
}

// A reload started while the previous load is still reading cancels it:
// the cancelled summary keeps the documents parsed so far, and the new
// generation replaces it once it ends.
func TestReload_SummaryCancelledMidLoad(t *testing.T) {
	m, _ := newReloadModel(t, `{"a": 1}`)

	// A generation-2 load with two documents and no end message.
	m.loadGen++
	m.eof = false
	m.loadDocs = 0
	m.loader = &loader{gen: m.loadGen, stop: make(chan struct{}), msgs: make(chan tea.Msg)}
	m.Update(nodeMsg{node: parseDoc(t, `{"x": 1}`), gen: m.loadGen, ok: true})
	m.Update(nodeMsg{node: parseDoc(t, `{"y": 2}`), gen: m.loadGen, ok: true})
	m.Update(nodeMsg{node: parseDoc(t, `"tail"`), gen: m.loadGen}) // recovered text: ok=false
	require.Equal(t, 2, m.loadDocs)

	// Reloading again stops generation 2 before it ended.
	cmd := m.reload()
	require.Equal(t, uint64(2), m.loadSummary.gen)
	require.True(t, m.loadSummary.cancelled)
	require.Equal(t, 2, m.loadSummary.docs)
	require.Zero(t, m.loadSummary.errLine)
	require.NotNil(t, m.message)
	require.Equal(t, "reload cancelled: 2 documents parsed", m.message.text)
	require.NotContains(t, m.message.text, "{", "the summary keeps no input content")

	// The new generation ends and its summary replaces the cancelled one.
	runAll(m, cmd)
	require.True(t, m.eof)
	require.Equal(t, m.loadGen, m.loadSummary.gen)
	require.Equal(t, 1, m.loadSummary.docs)
	require.False(t, m.loadSummary.cancelled)
	require.Equal(t, "reloaded: 1 document", m.message.text)
}

// End messages of a generation replaced by a reload must not overwrite
// the newer generation's summary, nor the message showing it.
func TestReload_StaleEndKeepsNewSummary(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1}`+"\n"+`{"b": 2}`)

	require.NoError(t, os.WriteFile(file, []byte(`[3]`), 0o644))
	reloadKey(m)
	require.Equal(t, m.loadGen, m.loadSummary.gen)
	require.Equal(t, 1, m.loadSummary.docs)
	msg := m.message

	stale := m.loadGen - 1
	m.Update(eofMsg{gen: stale})
	m.Update(errorMsg{err: os.ErrClosed, gen: stale})

	require.Equal(t, m.loadGen, m.loadSummary.gen)
	require.Equal(t, 1, m.loadSummary.docs)
	require.False(t, m.loadSummary.cancelled)
	require.Zero(t, m.loadSummary.errLine)
	require.Same(t, msg, m.message)
}

// Text recovered past a non-strict parse error is displayed but is not a
// successfully parsed document.
func TestReload_SummaryIgnoresRecoveredText(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1}`)

	require.NoError(t, os.WriteFile(file, []byte("HTTP/1.1 200 OK\n"+`{"a": 2}`), 0o644))
	reloadKey(m)

	require.True(t, m.eof)
	require.Equal(t, 1, m.loadSummary.docs, "recovered text is not a parsed document")
	require.Zero(t, m.loadSummary.errLine)
}

// The diagnostic summary does not interfere with restoring the cursor to
// its path in the reloaded document.
func TestReload_SummaryKeepsCursorRestore(t *testing.T) {
	m, file := newReloadModel(t, `{"a": 1, "b": {"c": 2}}`)
	m.selectNode(m.findByPath([]any{"b", "c"}))

	require.NoError(t, os.WriteFile(file, []byte(`{"a": 1, "b": {"c": 3}}`), 0o644))
	reloadKey(m)

	at, ok := m.cursorPointsTo()
	require.True(t, ok)
	require.Equal(t, "3", at.Value)
	require.Equal(t, ".b.c", m.cursorPath())
	require.Equal(t, 1, m.loadSummary.docs)
}

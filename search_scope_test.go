package main

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	. "github.com/antonmedv/fx/internal/jsonx"
)

// runSearchCmd executes the search command a key handler (the scope
// toggle) returned and delivers the search result to Update.
func runSearchCmd(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	require.NotNil(t, cmd, "the toggle started a search")
	batch, ok := cmd().(tea.BatchMsg)
	require.True(t, ok, "doSearch returns a batch")
	m.Update(batch[1]()) // The first command is the spinner tick.
}

// toggleScope presses ctrl+t and finishes the search it starts.
func toggleScope(t *testing.T, m *model) {
	t.Helper()
	_, cmd := m.Update(press("ctrl+t"))
	runSearchCmd(t, m, cmd)
}

// requireResultsInDoc asserts every match of the current search belongs to
// the same top-level document.
func requireResultsInDoc(t *testing.T, m *model, doc *Node) {
	t.Helper()
	require.NotEmpty(t, m.search.results)
	for _, r := range m.search.results {
		require.Same(t, doc, r.Root(), "result %q left its document", r.Key+r.Value)
	}
}

// requireSelectedFirstResult asserts the selection is on result 0 after a
// search.
func requireSelectedFirstResult(t *testing.T, m *model) {
	t.Helper()
	require.Zero(t, m.search.cursor, "the selection moved to the first result")
	at, ok := m.cursorPointsTo()
	require.True(t, ok)
	if at.IsWrap() {
		at = at.Parent
	}
	require.Same(t, m.search.results[0], at, "the cursor is on the first result")
}

const (
	scopeDoc1 = `{"id": 1, "v": "needle-one"}`
	scopeDoc2 = `{"id": 2, "v": "needle-two"}`
)

func TestSearchScope_TogglesBetweenDocumentAndAll(t *testing.T) {
	m := loadModel(t, scopeDoc1+"\n"+scopeDoc2, 80, 20)
	require.True(t, m.multipleDocs())
	require.Same(t, m.top, m.currentTopNode(), "the cursor opens in the first document")

	// Search every document first.
	finishSearch(t, m, startSearch(m, "needle"))
	require.Len(t, m.search.results, 2)
	roots := map[*Node]bool{}
	for _, r := range m.search.results {
		roots[r.Root()] = true
	}
	require.Len(t, roots, 2, "the all-document search covers both documents")

	// Limit the search to the document the cursor is in.
	toggleScope(t, m)
	require.True(t, m.searchCurrentDoc)
	require.Same(t, m.top, m.searchDoc)
	requireResultsInDoc(t, m, m.top)
	require.Len(t, m.search.results, 1)
	requireSelectedFirstResult(t, m)

	// Restore searching every document.
	toggleScope(t, m)
	require.False(t, m.searchCurrentDoc)
	require.Nil(t, m.searchDoc)
	require.Len(t, m.search.results, 2)
	requireSelectedFirstResult(t, m)
}

func TestSearchScope_ToggleSelectsFirstResult(t *testing.T) {
	input := `{"v": "needle-one"}` + "\n" +
		`{"a": "needle-x", "b": "needle-y", "c": "needle-z"}`
	m := loadModel(t, input, 80, 20)

	finishSearch(t, m, startSearch(m, "needle"))
	require.Len(t, m.search.results, 4)

	// Browse a few results; the cursor ends up inside the second document.
	keys(m, "n", "n")
	require.Same(t, m.bottom, m.currentTopNode().Root())

	// Switching the scope recalculates and jumps to the first match of the
	// current document, not the result browsed last.
	toggleScope(t, m)
	require.Len(t, m.search.results, 3)
	requireResultsInDoc(t, m, m.bottom)
	requireSelectedFirstResult(t, m)
	at, _ := m.cursorPointsTo()
	require.Contains(t, at.Key+at.Value, "needle-x")
}

func TestSearchScope_CurrentDocumentEmpty(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"empty object", `{}`},
		{"empty array", `[]`},
		{"null", `null`},
		{"false", `false`},
		{"scalar", `0`},
		{"empty string", `""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := loadModel(t, tc.doc+"\n"+`{"v": "needle"}`, 80, 20)
			keys(m, "g") // Loading followed the input to the bottom; go back to the first document.
			require.Same(t, m.top, m.currentTopNode())

			m.searchInput.SetValue("needle")
			_, cmd := m.Update(press("ctrl+t"))
			runSearchCmd(t, m, cmd)
			require.True(t, m.searchCurrentDoc)
			require.Empty(t, m.search.results, "the empty document has no matches")
			require.Contains(t, m.View().Content, "document not found")

			// Restoring the all-document scope finds the match next door.
			toggleScope(t, m)
			require.Len(t, m.search.results, 1)
			requireSelectedFirstResult(t, m)
		})
	}
}

func TestSearchScope_AllDocumentsEmpty(t *testing.T) {
	// Two empty documents: limiting the scope still works and finds nothing.
	m := loadModel(t, "{}\n[]", 80, 20)
	m.searchInput.SetValue("needle")
	_, cmd := m.Update(press("ctrl+t"))
	runSearchCmd(t, m, cmd)
	require.True(t, m.searchCurrentDoc)
	require.Empty(t, m.search.results)
	require.Contains(t, m.View().Content, "document not found")
}

func TestSearchScope_SingleDocumentIsNoop(t *testing.T) {
	m := loadModel(t, `{"v": "needle"}`, 80, 20)
	require.False(t, m.multipleDocs())

	// Open the search input and type a query: ctrl+t changes nothing with
	// one document, so no search starts while typing.
	m.Update(press("/"))
	typeKeys(m, "needle")
	_, cmd := m.Update(press("ctrl+t"))
	require.Nil(t, cmd)
	require.False(t, m.searchCurrentDoc)
	require.False(t, m.searching)
	require.Empty(t, m.search.results)
	require.NotContains(t, m.View().Content, "this document")
}

func TestSearchScope_ToggleWithoutSearchIsNoop(t *testing.T) {
	m := loadModel(t, scopeDoc1+"\n"+scopeDoc2, 80, 20)
	_, cmd := m.Update(press("ctrl+t"))
	require.Nil(t, cmd, "ctrl+t with no search does not start one")
	require.False(t, m.searchCurrentDoc)
}

// A document arriving while a current-document search runs is attached
// after the line the search stops on, so it cannot enter the scope.
func TestSearchScope_DocsStreamingWhileScopedSearching(t *testing.T) {
	m := loadModel(t, bigInput(), 40, 10)
	keys(m, "z")
	keys(m, "G") // Cursor in the last loaded document.
	pin := m.currentTopNode()
	require.NotNil(t, pin)
	require.NotSame(t, m.top, pin)

	m.searchCurrentDoc = true // doSearch pins the cursor's document.
	msgs := startSearch(m, "needle")
	for i := range 200 {
		node, err := Parse([]byte(fmt.Sprintf(`{"late": %d, "s": "needle"}`, i)))
		require.NoError(t, err)
		m.Update(nodeMsg{node: node})
	}
	require.True(t, m.searching, "streaming documents do not stop the search")
	finishSearch(t, m, msgs)
	require.False(t, m.searching)
	require.Len(t, m.search.results, 2, "only the two matches of the pinned document")
	requireResultsInDoc(t, m, pin)
	requireListSound(t, m)
}

// Pressing ctrl+t while a search is still running cancels it first, then
// searches the new scope: the stale result is dropped and the selection
// moves to the first result in the scope.
func TestSearchScope_ToggleWhileSearching(t *testing.T) {
	m := loadModel(t, bigInput(), 40, 10)
	keys(m, "z")

	msgs := startSearch(m, "needle") // All documents, cursor in the first.
	_, cmd := m.Update(press("ctrl+t"))
	finishSearch(t, m, msgs) // Result of the cancelled search is dropped.
	require.True(t, m.searching, "the scoped search keeps running")

	runSearchCmd(t, m, cmd)
	require.False(t, m.searching)
	require.True(t, m.searchCurrentDoc)
	require.Same(t, m.top, m.searchDoc)
	require.Len(t, m.search.results, 2)
	requireResultsInDoc(t, m, m.top)
	requireSelectedFirstResult(t, m)
	requireListSound(t, m)

	// Toggle back while the scoped result is shown: the new search covers
	// every loaded document.
	_, cmd = m.Update(press("ctrl+t"))
	runSearchCmd(t, m, cmd)
	require.False(t, m.searchCurrentDoc)
	require.Greater(t, len(m.search.results), 2)
	otherDoc := false
	for _, r := range m.search.results {
		if r.Root() != m.top {
			otherDoc = true
		}
	}
	require.True(t, otherDoc, "the restored search reaches other documents")
	requireSelectedFirstResult(t, m)
}

// Wrap and resize redo the search synchronously: the pinned document stays
// the scope, even though the cursor sits in it and more documents exist.
func TestSearchScope_RedoKeepsPinnedDocument(t *testing.T) {
	input := `{"v": "needle-one"}` + "\n" +
		`{"a": "needle-x", "b": "needle-y", "c": "needle-z"}`
	m := loadModel(t, input, 80, 20)
	keys(m, "G") // Move into the second document before scoping.
	require.Same(t, m.bottom, m.currentTopNode())

	m.searchInput.SetValue("needle")
	_, cmd := m.Update(press("ctrl+t"))
	runSearchCmd(t, m, cmd)
	require.Len(t, m.search.results, 3)
	requireResultsInDoc(t, m, m.bottom)

	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	require.True(t, m.searchCurrentDoc)
	require.Len(t, m.search.results, 3)
	requireResultsInDoc(t, m, m.bottom)

	keys(m, "z") // Turn wrapping on: redoSearch keeps the pinned scope.
	require.Len(t, m.search.results, 3)
	requireResultsInDoc(t, m, m.bottom)
}

// Toggling while typing recalculates immediately, and Enter keeps the
// chosen scope.
func TestSearchScope_ToggleWhileTypingAppliesImmediately(t *testing.T) {
	m := loadModel(t, scopeDoc1+"\n"+scopeDoc2, 80, 20)
	keys(m, "G") // Cursor in the second document.
	m.Update(press("/"))
	typeKeys(m, "needle")
	require.Contains(t, m.View().Content, "ctrl+t: this document")

	_, cmd := m.Update(press("ctrl+t"))
	runSearchCmd(t, m, cmd) // Matches are recalculated at once.
	require.True(t, m.searchCurrentDoc)
	require.Same(t, m.bottom, m.searchDoc)
	require.Len(t, m.search.results, 1)
	requireResultsInDoc(t, m, m.bottom)
	requireSelectedFirstResult(t, m)
	require.Contains(t, m.View().Content, "this document")

	_, cmd = m.Update(press("enter")) // Submitting searches the same scope.
	runSearchCmd(t, m, cmd)
	require.False(t, m.searching)
	require.True(t, m.searchCurrentDoc)
	require.Same(t, m.bottom, m.searchDoc)
	require.Len(t, m.search.results, 1)
	requireResultsInDoc(t, m, m.bottom)
}

func TestSearchScope_HelpListsBinding(t *testing.T) {
	require.Contains(t, help(keyMap), "toggle search this document")
}

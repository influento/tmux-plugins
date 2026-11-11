// Package layout reads and writes tmux 3.8 window layouts, which are JSON
// (#{window_layout}, select-layout). It removes panes from a layout and
// scales a layout to a window size: select-layout applies the cell sizes
// as given and does not scale them to the window.
//
// select-layout gives each pane cell to the pane its "I" names, so a
// layout keeps its panes in place whatever their order in the window.
package layout

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Cell types: a pane, or a split of cells side by side (h) or one above
// the other (v).
const (
	Pane       = "p"
	Horizontal = "h"
	Vertical   = "v"
)

// Layout is a window's layout.
type Layout struct {
	Version int  `json:"V"`
	Root    Cell `json:"L"`
}

// Cell is a pane or a split in a layout. Sizes and positions are in
// terminal cells; a border of one cell separates the children of a split.
type Cell struct {
	Type   string `json:"t"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Active bool   `json:"a,omitempty"`
	Last   *int   `json:"l,omitempty"`
	Index  *int   `json:"i,omitempty"` // select-layout needs it on a pane
	ID     string `json:"I,omitempty"` // the pane's id, %N
	Cells  []Cell `json:"c,omitempty"`
}

// Parse reads a layout.
func Parse(s string) (Layout, error) {
	var l Layout
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		return Layout{}, fmt.Errorf("layout: %w", err)
	}
	if l.Root.Type == "" {
		return Layout{}, errors.New("layout: no root cell")
	}
	return l, nil
}

// String is the layout as select-layout takes it.
func (l Layout) String() string {
	b, err := json.Marshal(l)
	if err != nil {
		panic(err) // the types above always marshal
	}
	return string(b)
}

// Panes are the ids of the layout's panes, sorted.
func (l Layout) Panes() []string {
	var ids []string
	var walk func(c Cell)
	walk = func(c Cell) {
		if c.Type == Pane {
			ids = append(ids, c.ID)
		}
		for _, k := range c.Cells {
			walk(k)
		}
	}
	walk(l.Root)
	slices.Sort(ids)
	return ids
}

// ActivePane is the id of the pane marked active; "" for none.
func (l Layout) ActivePane() string {
	var walk func(c Cell) string
	walk = func(c Cell) string {
		if c.Type == Pane && c.Active {
			return c.ID
		}
		for _, k := range c.Cells {
			if id := walk(k); id != "" {
				return id
			}
		}
		return ""
	}
	return walk(l.Root)
}

// Even is the layout with panes a and b the same size, where they share a
// split: their widths in a split side by side, their heights in one above
// the other. Elsewhere it is the layout as it was.
func (l Layout) Even(a, b string) Layout {
	l.Root = even(l.Root, a, b)
	return l
}

func even(c Cell, a, b string) Cell {
	if c.Type == Pane {
		return c
	}
	kids := make([]Cell, len(c.Cells))
	ia, ib := -1, -1
	for i, k := range c.Cells {
		kids[i] = even(k, a, b)
		if k.Type == Pane && k.ID == a {
			ia = i
		}
		if k.Type == Pane && k.ID == b {
			ib = i
		}
	}
	if ia >= 0 && ib >= 0 {
		if c.Type == Horizontal {
			n := (kids[ia].W + kids[ib].W) / 2
			kids[ia].W, kids[ib].W = n, n
		} else {
			n := (kids[ia].H + kids[ib].H) / 2
			kids[ia].H, kids[ib].H = n, n
		}
	}
	c.Cells = kids
	return c
}

// Without is the layout with the panes ids taken out: a split left with
// one child becomes that child. ok is false when no pane is left. Sizes
// stay as they were; Scale fits them to a window.
func (l Layout) Without(ids ...string) (out Layout, ok bool) {
	root, ok := strip(l.Root, ids)
	if !ok {
		return Layout{}, false
	}
	return Layout{Version: l.Version, Root: root}, true
}

func strip(c Cell, ids []string) (Cell, bool) {
	if c.Type == Pane {
		return c, !slices.Contains(ids, c.ID)
	}
	var kids []Cell
	for _, k := range c.Cells {
		if k, ok := strip(k, ids); ok {
			kids = append(kids, k)
		}
	}
	switch len(kids) {
	case 0:
		return Cell{}, false
	case 1:
		return kids[0], true
	}
	c.Cells = kids
	return c, true
}

// Inactive is the layout with no pane marked active.
func (l Layout) Inactive() Layout {
	l.Root = inactive(l.Root)
	return l
}

func inactive(c Cell) Cell {
	c.Active = false
	if len(c.Cells) > 0 {
		kids := make([]Cell, len(c.Cells))
		for i, k := range c.Cells {
			kids[i] = inactive(k)
		}
		c.Cells = kids
	}
	return c
}

// Scale is the layout fitted to a w×h window at the top left, each split
// keeping its children's proportions, its panes numbered again.
func (l Layout) Scale(w, h int) Layout {
	l.Root = ScaleCell(l.Root, 0, 0, w, h)
	return l.Numbered()
}

// Numbered is the layout with its panes' indexes 0, 1, ... in order:
// select-layout refuses two panes with one index.
func (l Layout) Numbered() Layout {
	n := 0
	var walk func(c Cell) Cell
	walk = func(c Cell) Cell {
		if c.Type == Pane {
			i := n
			n++
			c.Index = &i
			return c
		}
		kids := make([]Cell, len(c.Cells))
		for i, k := range c.Cells {
			kids[i] = walk(k)
		}
		c.Cells = kids
		return c
	}
	l.Root = walk(l.Root)
	return l
}

// ScaleCell is the cell fitted to w×h at x, y.
func ScaleCell(c Cell, x, y, w, h int) Cell {
	c.X, c.Y, c.W, c.H = x, y, w, h
	if c.Type == Pane || len(c.Cells) == 0 {
		return c
	}
	kids := make([]Cell, len(c.Cells))
	if c.Type == Horizontal {
		weights := make([]int, len(c.Cells))
		for i, k := range c.Cells {
			weights[i] = k.W
		}
		at := x
		for i, s := range Share(w, weights) {
			kids[i] = ScaleCell(c.Cells[i], at, y, s, h)
			at += s + 1
		}
	} else {
		weights := make([]int, len(c.Cells))
		for i, k := range c.Cells {
			weights[i] = k.H
		}
		at := y
		for i, s := range Share(h, weights) {
			kids[i] = ScaleCell(c.Cells[i], x, at, w, s)
			at += s + 1
		}
	}
	c.Cells = kids
	return c
}

// Share hands total cells out by weights, less one border between each
// two: each gets at least 1 and they add up to total minus the borders
// when there is room. The rounding rest goes to the largest remainders.
func Share(total int, weights []int) []int {
	n := len(weights)
	if n == 0 {
		return nil
	}
	room := total - (n - 1)
	sum := 0
	for _, w := range weights {
		sum += max(w, 0)
	}
	sizes := make([]int, n)
	rems := make([]int, n)
	for i, w := range weights {
		if sum == 0 {
			sizes[i], rems[i] = room/n, 0
		} else {
			sizes[i], rems[i] = max(w, 0)*room/sum, max(w, 0)*room%sum
		}
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return rems[b] - rems[a] })
	rest := room
	for _, s := range sizes {
		rest -= s
	}
	for i := 0; rest > 0; i++ {
		sizes[order[i%n]]++
		rest--
	}
	// Each at least 1, taken from the largest.
	for i := range sizes {
		for sizes[i] < 1 {
			j := slices.Index(sizes, slices.Max(sizes))
			if sizes[j] <= 1 {
				sizes[i] = 1 // no room: tmux refuses the layout
				break
			}
			sizes[j]--
			sizes[i]++
		}
	}
	return sizes
}

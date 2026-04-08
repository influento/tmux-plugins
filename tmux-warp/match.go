package main

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// Position represents a match location in the pane (rune-based column).
type Position struct {
	Row int
	Col int
}

// Match represents a found match with its position and assigned label.
type Match struct {
	Pos   Position
	Label string
}

// labelKeys are home-row keys used for labels, ordered by ergonomic preference.
var labelKeys = []byte("jfhgkdlsa")

// maxLabelsThreshold is the match count at or below which we enter label-select mode.
const maxLabelsThreshold = 9

// FindMatches finds all positions where query appears in the pane content.
// Positions use rune-based column indices.
func FindMatches(content []string, query string) []Position {
	if query == "" {
		return nil
	}
	lowerQuery := strings.ToLower(query)
	var positions []Position
	for row, line := range content {
		lowerLine := strings.ToLower(line)
		byteStart := 0
		for {
			idx := strings.Index(lowerLine[byteStart:], lowerQuery)
			if idx < 0 {
				break
			}
			bytePos := byteStart + idx
			runeCol := utf8.RuneCountInString(line[:bytePos])
			positions = append(positions, Position{Row: row, Col: runeCol})
			byteStart = bytePos + 1
		}
	}
	return positions
}

// FindCharMatches finds all positions of a single character in the pane content.
// Positions use rune-based column indices.
func FindCharMatches(content []string, ch byte) []Position {
	target := strings.ToLower(string(ch))
	var positions []Position
	for row, line := range content {
		for col, r := range line {
			if strings.ToLower(string(r)) == target {
				runeCol := utf8.RuneCountInString(line[:col])
				positions = append(positions, Position{Row: row, Col: runeCol})
			}
		}
	}
	return positions
}

// AssignLabels assigns labels to matches sorted by distance from cursor.
// skipChars contains characters that should not be used as labels.
func AssignLabels(positions []Position, cursorX, cursorY int, skipChars string) []Match {
	if len(positions) == 0 {
		return nil
	}

	type posWithDist struct {
		pos  Position
		dist float64
	}

	items := make([]posWithDist, len(positions))
	for i, p := range positions {
		dx := float64(p.Col - cursorX)
		dy := float64(p.Row - cursorY)
		items[i] = posWithDist{pos: p, dist: math.Sqrt(dx*dx + dy*dy)}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].dist < items[j].dist
	})

	skipLower := strings.ToLower(skipChars)
	var availKeys []byte
	for _, k := range labelKeys {
		if !strings.ContainsRune(skipLower, rune(k)) {
			availKeys = append(availKeys, k)
		}
	}

	labels := generateLabels(len(items), availKeys)

	matches := make([]Match, len(items))
	for i, item := range items {
		label := ""
		if i < len(labels) {
			label = labels[i]
		}
		matches[i] = Match{Pos: item.pos, Label: label}
	}
	return matches
}

// generateLabels creates enough label strings for n matches using the given keys.
func generateLabels(n int, keys []byte) []string {
	if len(keys) == 0 {
		return nil
	}

	var labels []string

	for _, k := range keys {
		labels = append(labels, string(k))
		if len(labels) >= n {
			return labels[:n]
		}
	}

	for _, k1 := range keys {
		for _, k2 := range keys {
			labels = append(labels, string(k1)+string(k2))
			if len(labels) >= n {
				return labels[:n]
			}
		}
	}

	return labels
}

// MatchMap builds a lookup from Position to Match for quick rendering.
func MatchMap(matches []Match) map[Position]Match {
	m := make(map[Position]Match, len(matches))
	for _, match := range matches {
		m[match.Pos] = match
	}
	return m
}

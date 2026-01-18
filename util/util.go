package util

import (
	"os"
	"path/filepath"

	"github.com/agnivade/levenshtein"
)

// FindProjectRoot finds the project root directory by looking for a specific file or directory that should be at the root.
func FindProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parentDir := filepath.Dir(dir)
		if parentDir == dir {
			break
		}
		dir = parentDir
	}

	return "", os.ErrNotExist
}

// LevenshteinDistance calculates the Levenshtein distance between two strings using optimized library
func LevenshteinDistance(a, b string) int {
	return levenshtein.ComputeDistance(a, b)
}

// LevenshteinDistanceWithThreshold calculates Levenshtein distance with early termination
// Returns distance and true if distance <= threshold, or threshold+1 and false if distance > threshold
func LevenshteinDistanceWithThreshold(a, b string, threshold int) (int, bool) {
	dist := levenshtein.ComputeDistance(a, b)
	return dist, dist <= threshold
}

func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// BKTree is a data structure for fast fuzzy string matching
type BKTree struct {
	Root *bkNode
}

type bkNode struct {
	Term     string
	Children map[int]*bkNode
}

// NewBKTree creates a new BK-tree
func NewBKTree() *BKTree {
	return &BKTree{}
}

// Add inserts a term into the BK-tree
func (tree *BKTree) Add(term string) {
	if tree.Root == nil {
		tree.Root = &bkNode{Term: term, Children: make(map[int]*bkNode)}
		return
	}
	current := tree.Root
	for {
		distance := LevenshteinDistance(term, current.Term)
		child, exists := current.Children[distance]
		if !exists {
			current.Children[distance] = &bkNode{Term: term, Children: make(map[int]*bkNode)}
			return
		}
		current = child
	}
}

// Search returns terms in the BK-tree within the given distance of the query term
func (tree *BKTree) Search(query string, maxDistance int) []string {
	if tree.Root == nil {
		return nil
	}
	var results []string
	var search func(*bkNode)
	search = func(node *bkNode) {
		distance := LevenshteinDistance(query, node.Term)
		if distance <= maxDistance {
			results = append(results, node.Term)
		}
		for i := max(1, distance-maxDistance); i <= distance+maxDistance; i++ {
			child, exists := node.Children[i]
			if exists {
				search(child)
			}
		}
	}
	search(tree.Root)
	return results
}

// SearchWithEarlyExit is an optimized version that uses early termination
// when distance exceeds maxDistance to potentially skip entire subtrees
func (tree *BKTree) SearchWithEarlyExit(query string, maxDistance int) []string {
	if tree.Root == nil {
		return nil
	}
	var results []string
	var search func(*bkNode, int) // second param is min possible distance to this subtree
	search = func(node *bkNode, minDist int) {
		// Early exit if minimum possible distance to this subtree exceeds maxDistance
		if minDist > maxDistance {
			return
		}

		distance := LevenshteinDistance(query, node.Term)
		if distance <= maxDistance {
			results = append(results, node.Term)
		}

		// Prune subtrees where min distance to subtree root would exceed threshold
		for i := max(1, distance-maxDistance); i <= distance+maxDistance; i++ {
			child, exists := node.Children[i]
			if exists {
				// Calculate minimum possible distance to this child subtree
				minChildDist := Abs(i - distance)
				search(child, minChildDist)
			}
		}
	}
	search(tree.Root, 0)
	return results
}

// Max returns the maximum of two integers
func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Abs returns the absolute value of an integer
func Abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

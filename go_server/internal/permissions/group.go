package permissions

import (
	"errors"
	"fmt"
	"sort"
)

// MaxGroupDepth is the deepest a group may sit, counting the root as level one,
// matching Storage.MAX_GROUP_DEPTH in the Java server. A grant on a group
// reaches that group and every group below it inside that budget, so a three
// level tree (root, child, grandchild) is fully reachable from a grant on the
// root and a fourth level is not allowed at all.
const MaxGroupDepth = 3

// Group errors, shared by the REST layer and the storage layer.
var (
	// ErrGroupDepth is returned when a group edit would put a group below the
	// fourth level, or would make a group its own ancestor.
	ErrGroupDepth = errors.New("maximum group depth exceeded")
	// ErrUnknownGroup is returned when a permission or a hierarchy check names
	// a group that does not exist.
	ErrUnknownGroup = errors.New("unknown group")
)

// Reach returns the granted groups together with every descendant inside
// MaxGroupDepth, sorted, without duplicates.
//
// parents maps a group id to its parent; a root maps to zero. A grant on a root
// therefore covers the root, its children and its grandchildren, which is what
// the Java server's recursive condition does. A cycle in the tree cannot loop
// here: a group is queued at most once.
func Reach(grants []int64, parents map[int64]int64) []int64 {
	children := childrenOf(parents)

	seen := make(map[int64]struct{}, len(grants))
	queue := make([]int64, 0, len(grants))
	for _, grant := range grants {
		if grant <= 0 {
			continue
		}
		if _, ok := seen[grant]; ok {
			continue
		}
		seen[grant] = struct{}{}
		queue = append(queue, grant)
	}

	// The granted group is level one, so two more levels fit in MaxGroupDepth.
	for depth := 1; depth < MaxGroupDepth; depth++ {
		next := make([]int64, 0, len(queue))
		for _, groupID := range queue {
			for _, child := range children[groupID] {
				if _, ok := seen[child]; ok {
					continue
				}
				seen[child] = struct{}{}
				next = append(next, child)
			}
		}
		if len(next) == 0 {
			break
		}
		queue = next
	}

	reached := make([]int64, 0, len(seen))
	for groupID := range seen {
		reached = append(reached, groupID)
	}
	sort.Slice(reached, func(i, j int) bool { return reached[i] < reached[j] })
	return reached
}

// childrenOf inverts a parent map. Entries that point at themselves or at a
// non-positive id are roots and contribute no edge.
func childrenOf(parents map[int64]int64) map[int64][]int64 {
	children := make(map[int64][]int64, len(parents))
	for groupID, parentID := range parents {
		if parentID <= 0 || parentID == groupID {
			continue
		}
		children[parentID] = append(children[parentID], groupID)
	}
	return children
}

// Depth reports how many levels a group sits below its root: a root is zero, its
// child one, its grandchild two. A group caught in a cycle reports -1.
func Depth(parents map[int64]int64, groupID int64) int {
	depth := 0
	seen := make(map[int64]struct{}, MaxGroupDepth+1)
	for groupID > 0 {
		if _, ok := seen[groupID]; ok {
			return -1
		}
		seen[groupID] = struct{}{}
		parentID, ok := parents[groupID]
		if !ok || parentID <= 0 || parentID == groupID {
			return depth
		}
		groupID = parentID
		depth++
	}
	return depth
}

// CheckHierarchy reports whether groupID may be given parentID as its parent.
// A group must not become its own ancestor, and every group must stay within
// MaxGroupDepth once the edge is in place.
//
// parents is the tree as it is now. For an insert, pass zero as groupID and the
// parent the new row would take; for an update, pass the row being edited.
func CheckHierarchy(parents map[int64]int64, groupID, parentID int64) error {
	if parentID <= 0 {
		return nil
	}
	if _, ok := parents[parentID]; !ok {
		return fmt.Errorf("%w: %d", ErrUnknownGroup, parentID)
	}
	if groupID != 0 && groupID == parentID {
		return fmt.Errorf("%w: a group cannot be its own parent", ErrGroupDepth)
	}

	updated := make(map[int64]int64, len(parents)+1)
	for id, parent := range parents {
		updated[id] = parent
	}
	if groupID != 0 {
		updated[groupID] = parentID
	}

	// Checking the whole tree keeps the rule in one place instead of special
	// casing the edge that changed, and it also refuses to keep a tree that was
	// already too deep.
	for id := range updated {
		if depth := Depth(updated, id); depth < 0 || depth >= MaxGroupDepth {
			return fmt.Errorf("%w: group %d would sit at level %d", ErrGroupDepth, id, depth+1)
		}
	}
	if groupID == 0 {
		// The new row itself hangs one level below its parent.
		if depth := Depth(updated, parentID) + 1; depth >= MaxGroupDepth {
			return fmt.Errorf("%w: the new group would sit at level %d", ErrGroupDepth, depth+1)
		}
	}
	return nil
}

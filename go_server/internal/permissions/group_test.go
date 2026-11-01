package permissions

import (
	"errors"
	"reflect"
	"testing"
)

// threeLevelTree is root(1) -> child(2) -> grandchild(3), plus a second root(4)
// with two children (5, 6).
func threeLevelTree() map[int64]int64 {
	return map[int64]int64{
		1: 0,
		2: 1,
		3: 2,
		4: 0,
		5: 4,
		6: 4,
	}
}

func TestReachFromRoot(t *testing.T) {
	got := Reach([]int64{1}, threeLevelTree())
	want := []int64{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Reach = %v, want %v", got, want)
	}
}

func TestReachStopsAtMaxDepth(t *testing.T) {
	parents := threeLevelTree()
	// A fourth level is not reachable from the root even though it exists.
	parents[7] = 3

	got := Reach([]int64{1}, parents)
	want := []int64{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Reach = %v, want %v", got, want)
	}

	// A grant on the child covers the child and the grandchild but not the
	// great-grandchild either.
	got = Reach([]int64{2}, parents)
	want = []int64{2, 3, 7}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Reach from child = %v, want %v", got, want)
	}
}

func TestReachMergesGrants(t *testing.T) {
	got := Reach([]int64{5, 1, 5, 0, -3}, threeLevelTree())
	want := []int64{1, 2, 3, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Reach = %v, want %v", got, want)
	}
}

func TestReachSurvivesCycle(t *testing.T) {
	parents := map[int64]int64{1: 0, 2: 3, 3: 2}
	got := Reach([]int64{1}, parents)
	if !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("Reach = %v, want just the granted group", got)
	}

	got = Reach([]int64{2}, parents)
	if !reflect.DeepEqual(got, []int64{2, 3}) {
		t.Fatalf("Reach = %v, want the two cycle members", got)
	}
}

func TestReachTreatsSelfParentAsRoot(t *testing.T) {
	parents := map[int64]int64{1: 1, 2: 1}
	if got := Reach([]int64{1}, parents); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("Reach = %v, want [1 2]", got)
	}
}

func TestDepth(t *testing.T) {
	parents := threeLevelTree()
	cases := map[int64]int{1: 0, 2: 1, 3: 2, 4: 0, 5: 1}
	for groupID, want := range cases {
		if got := Depth(parents, groupID); got != want {
			t.Errorf("Depth(%d) = %d, want %d", groupID, got, want)
		}
	}
	if got := Depth(parents, 99); got != 0 {
		t.Errorf("Depth(unknown) = %d, want 0", got)
	}
	if got := Depth(map[int64]int64{1: 2, 2: 1}, 1); got != -1 {
		t.Errorf("Depth in a cycle = %d, want -1", got)
	}
}

func TestCheckHierarchyAllowsThreeLevels(t *testing.T) {
	parents := map[int64]int64{1: 0, 2: 1}
	// A third level is exactly what MaxGroupDepth allows.
	if err := CheckHierarchy(parents, 0, 2); err != nil {
		t.Fatalf("third level rejected: %v", err)
	}
	if err := CheckHierarchy(map[int64]int64{1: 0, 2: 1, 3: 2}, 3, 2); err != nil {
		t.Fatalf("reparenting at the same depth rejected: %v", err)
	}
}

func TestCheckHierarchyRejectsFourthLevel(t *testing.T) {
	parents := map[int64]int64{1: 0, 2: 1, 3: 2}
	if err := CheckHierarchy(parents, 0, 3); !errors.Is(err, ErrGroupDepth) {
		t.Fatalf("err = %v, want ErrGroupDepth", err)
	}
	// Moving the root under the grandchild would push everything down a level.
	if err := CheckHierarchy(parents, 1, 3); !errors.Is(err, ErrGroupDepth) {
		t.Fatalf("err = %v, want ErrGroupDepth", err)
	}
}

func TestCheckHierarchyRejectsSelfParentAndCycle(t *testing.T) {
	parents := threeLevelTree()
	if err := CheckHierarchy(parents, 1, 1); !errors.Is(err, ErrGroupDepth) {
		t.Errorf("self parent: err = %v, want ErrGroupDepth", err)
	}
	// Making the root a child of its own grandchild closes a cycle.
	if err := CheckHierarchy(parents, 1, 3); !errors.Is(err, ErrGroupDepth) {
		t.Errorf("cycle: err = %v, want ErrGroupDepth", err)
	}
}

func TestCheckHierarchyUnknownParent(t *testing.T) {
	if err := CheckHierarchy(threeLevelTree(), 0, 99); !errors.Is(err, ErrUnknownGroup) {
		t.Errorf("err = %v, want ErrUnknownGroup", err)
	}
	if err := CheckHierarchy(threeLevelTree(), 1, 99); !errors.Is(err, ErrUnknownGroup) {
		t.Errorf("err = %v, want ErrUnknownGroup", err)
	}
}

func TestCheckHierarchyWithoutParent(t *testing.T) {
	if err := CheckHierarchy(threeLevelTree(), 3, 0); err != nil {
		t.Errorf("moving a group to the root: %v", err)
	}
}

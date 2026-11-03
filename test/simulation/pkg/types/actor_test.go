package types

import (
	"bytes"
	"encoding/json"
	"errors"
	rand "math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"
)

func Test(t *testing.T) { TestingT(t) }

type ActorSuite struct{}

var _ = Suite(&ActorSuite{})

func testRNG() *rand.Rand {
	return rand.New(rand.NewChaCha8([32]byte{}))
}

func (s *ActorSuite) TestInit(c *C) {
	rng := testRNG()
	root := NewActor("root", rng)
	child1 := NewActor("child1", rng)
	child2 := NewActor("child2", rng)
	grandchild1 := NewActor("grandchild1", rng)
	grandchild2 := NewActor("grandchild2", rng)

	root.Children[child1] = true
	root.Children[child2] = true
	child1.Children[grandchild1] = true
	child2.Children[grandchild2] = true

	root.InitRoot()

	// init the atomics
	c.Assert(child1.state, NotNil)
	c.Assert(grandchild2.state, NotNil)

	// ensure parents are set
	c.Assert(child1.parents, DeepEquals, map[*Actor]bool{root: true})
	c.Assert(grandchild2.parents, DeepEquals, map[*Actor]bool{child2: true})
}

func (s *ActorSuite) TestWalkDepthFirst(c *C) {
	rng := testRNG()
	root := NewActor("root", rng)
	child1 := NewActor("child1", rng)
	child2 := NewActor("child2", rng)
	grandchild1 := NewActor("grandchild1", rng)
	grandchild2 := NewActor("grandchild2", rng)

	root.Children[child1] = true
	root.Children[child2] = true
	child1.Children[grandchild1] = true
	child2.Children[grandchild2] = true

	root.InitRoot()

	visited := map[string]bool{}
	root.WalkDepthFirst(func(a *Actor) bool {
		visited[a.Name] = true
		return a.Execute(nil) == nil
	})

	expected := map[string]bool{
		"root":        true,
		"child1":      true,
		"grandchild1": true,
		"child2":      true,
		"grandchild2": true,
	}
	c.Assert(visited, DeepEquals, expected)
}

func (s *ActorSuite) TestWalkDepthFirstFail(c *C) {
	rng := testRNG()
	opFail := func(config *OpConfig) OpResult {
		return OpResult{Continue: false, Finish: true, Error: errors.New("foo")}
	}

	root := NewActor("root", rng)
	child1 := NewActor("child1", rng)
	child2 := NewActor("child2", rng)
	child2.Ops = []Op{opFail}
	child3 := NewActor("child3", rng)
	grandchild1 := NewActor("grandchild1", rng)
	grandchild2 := NewActor("grandchild2", rng)
	grandchild3 := NewActor("grandchild3", rng)

	root.Children[child1] = true
	root.Children[child2] = true
	root.Children[child3] = true
	child1.Children[grandchild1] = true
	child2.Children[grandchild2] = true
	child3.Children[grandchild3] = true

	root.InitRoot()

	visited := map[string]bool{}
	root.WalkDepthFirst(func(a *Actor) bool {
		visited[a.Name] = true
		return a.Execute(nil) == nil
	})

	expected := map[string]bool{
		"root":        true,
		"child1":      true,
		"child2":      true,
		"child3":      true,
		"grandchild1": true,
		"grandchild3": true,
	}
	c.Assert(visited, DeepEquals, expected)
}

func (s *ActorSuite) TestWalkDepthFirstVisitsDAGNodeOnce(c *C) {
	rng := testRNG()
	root := NewActor("root", rng)
	parent1 := NewActor("parent1", rng)
	parent2 := NewActor("parent2", rng)
	shared := NewActor("shared", rng)
	child := NewActor("child", rng)

	root.Children[parent1] = true
	root.Children[parent2] = true
	parent1.Children[shared] = true
	parent2.Children[shared] = true
	shared.Children[child] = true

	root.InitRoot()

	visits := map[string]int{}
	cont := root.WalkDepthFirst(func(a *Actor) bool {
		visits[a.Name]++
		return true
	})

	expected := map[string]int{
		"root":    1,
		"parent1": 1,
		"parent2": 1,
		"shared":  1,
		"child":   1,
	}
	c.Assert(cont, Equals, true)
	c.Assert(visits, DeepEquals, expected)
	c.Assert(shared.parents, DeepEquals, map[*Actor]bool{parent1: true, parent2: true})
	c.Assert(child.parents, DeepEquals, map[*Actor]bool{shared: true})
}

// TestTimeoutReportsTimeSpentPerOp checks that a timeout log shows how much of
// the budget the stalled op used, separating slow setup from slow verification.
func (s *ActorSuite) TestTimeoutReportsTimeSpentPerOp(c *C) {
	actor := NewActor("timeout", testRNG())
	actor.Timeout = 30 * time.Millisecond
	actor.Interval = time.Millisecond
	var buf bytes.Buffer
	actor.SetLogger(zerolog.New(&buf))

	setup := func(*OpConfig) OpResult {
		time.Sleep(20 * time.Millisecond)
		return OpResult{Continue: true}
	}
	verify := func(*OpConfig) OpResult { return OpResult{} }
	actor.Ops = []Op{setup, verify}

	c.Assert(actor.Execute(&OpConfig{}), Equals, ErrTimeout)

	var entry struct {
		Op        string  `json:"op"`
		OpElapsed float64 `json:"op_elapsed"`
		Elapsed   float64 `json:"elapsed"`
		Timeout   float64 `json:"timeout"`
	}
	c.Assert(json.Unmarshal(buf.Bytes(), &entry), IsNil)
	c.Assert(strings.Contains(entry.Op, "TestTimeoutReportsTimeSpentPerOp"), Equals, true)
	c.Assert(entry.Timeout, Equals, float64(30))
	c.Assert(entry.Elapsed > entry.Timeout, Equals, true)
	c.Assert(entry.OpElapsed < entry.Elapsed-15, Equals, true)
}

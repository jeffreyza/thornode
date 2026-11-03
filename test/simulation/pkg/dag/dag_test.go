package dag

import (
	rand "math/rand/v2"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestExecute(t *testing.T) {
	// NewUser reads the Bifrost chain ID from the loaded config.
	t.Cleanup(config.Init) // t.Setenv restores CHAIN_ID before this cleanup runs.
	t.Setenv("CHAIN_ID", "thorchain-mocknet-v1")
	config.Init()

	rng := rand.New(rand.NewPCG(1, 2))

	// bump count once on each operation
	count := 0
	op := func(config *OpConfig) OpResult {
		count++
		return OpResult{Continue: true, Finish: true, Error: nil}
	}

	// create nodes
	root := NewActor("root", rng)
	child1 := NewActor("child1", rng)
	child2 := NewActor("child2", rng)
	child3 := NewActor("child3", rng)
	grandchild1 := NewActor("grandchild1", rng)
	grandchild2 := NewActor("grandchild2", rng)
	grandchild3 := NewActor("grandchild3", rng)

	// add operations
	descendants := []*Actor{child1, child2, child3, grandchild1, grandchild2, grandchild3}
	for _, node := range descendants {
		node.Ops = []Op{op}
	}

	// build dag
	root.Children[child1] = true
	root.Children[child2] = true
	root.Children[child3] = true
	child1.Children[grandchild1] = true
	child2.Children[grandchild2] = true
	child3.Children[grandchild3] = true

	// execute
	cfg := &OpConfig{
		Users: []*User{
			NewUser(strings.Repeat("dog ", 23)+"fossil", nil),
		},
	}
	Execute(cfg, root, 1, rng)

	// should have executed op 6 times
	if count != 6 {
		t.Fatalf("expected 6 operations, got %d", count)
	}
}

func TestExecuteIgnoresBackgroundActorsAtShutdown(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))

	releaseBackground := make(chan struct{})
	childRan := make(chan struct{})
	executeDone := make(chan struct{})

	background := NewActor("background", rng)
	child := NewActor("child", rng)
	root := NewActor("root", rng)

	background.Ops = []Op{
		func(config *OpConfig) OpResult {
			background.Background()
			return OpResult{Continue: true}
		},
		func(config *OpConfig) OpResult {
			<-releaseBackground
			return OpResult{Finish: true}
		},
	}
	child.Ops = []Op{
		func(config *OpConfig) OpResult {
			close(childRan)
			return OpResult{Finish: true}
		},
	}

	root.Children[background] = true
	background.Children[child] = true

	go func() {
		Execute(nil, root, 2, rng)
		close(executeDone)
	}()

	select {
	case <-childRan:
	case <-time.After(5 * time.Second):
		t.Fatal("child actor never ran")
	}

	select {
	case <-executeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("execute waited for background actor to finish")
	}

	close(releaseBackground)
}

func TestExecuteBackgroundActorDoesNotConsumeParallelism(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))

	releaseBackground := make(chan struct{})
	childRan := make(chan struct{})
	executeDone := make(chan struct{})

	background := NewActor("background", rng)
	child := NewActor("child", rng)
	root := NewActor("root", rng)

	background.Ops = []Op{
		func(config *OpConfig) OpResult {
			background.Background()
			return OpResult{Continue: true}
		},
		func(config *OpConfig) OpResult {
			<-releaseBackground
			return OpResult{Finish: true}
		},
	}
	child.Ops = []Op{
		func(config *OpConfig) OpResult {
			close(childRan)
			return OpResult{Finish: true}
		},
	}

	root.Children[background] = true
	background.Children[child] = true

	go func() {
		Execute(nil, root, 1, rng)
		close(executeDone)
	}()

	select {
	case <-childRan:
	case <-time.After(5 * time.Second):
		t.Fatal("child actor never ran after parent backgrounded")
	}

	select {
	case <-executeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("execute waited for background actor to release a foreground slot")
	}

	close(releaseBackground)
}

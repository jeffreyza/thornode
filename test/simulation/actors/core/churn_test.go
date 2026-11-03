package core

import (
	rand "math/rand/v2"
	"strings"
	"testing"
)

func TestChurnActorHonorsCallerSpecificSkip(t *testing.T) {
	a := newChurnActor(rand.New(rand.NewPCG(1, 2)), func() string {
		return "bond-yield requires mocknet cluster"
	})

	result := a.Ops[0](nil)
	if !result.Finish || result.Error != nil {
		t.Fatalf("expected caller-specific skip to finish successfully, got %+v", result)
	}
}

func TestChurnNodeCountErrorFailsClosed(t *testing.T) {
	err := churnNodeCountError(1)
	if err == nil || !strings.Contains(err.Error(), "at least two") {
		t.Fatalf("expected insufficient node count error, got %v", err)
	}
	if err := churnNodeCountError(2); err != nil {
		t.Fatalf("expected two nodes to pass validation, got %v", err)
	}
}

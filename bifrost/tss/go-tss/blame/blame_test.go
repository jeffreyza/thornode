package blame

import (
	"fmt"
	"sync"

	. "gopkg.in/check.v1"
)

type BlameTestSuite struct{}

var _ = Suite(&BlameTestSuite{})

func createNewNode(key string) Node {
	return Node{
		Pubkey:         key,
		BlameData:      nil,
		BlameSignature: nil,
	}
}

func (BlameTestSuite) TestBlame(c *C) {
	b := NewBlame("whatever", []Node{createNewNode("1"), createNewNode("2")})
	c.Assert(b.FailReason, HasLen, 8)
	c.Logf("%s", b)
	b.AddBlameNodes(createNewNode("3"), createNewNode("4"))
	c.Assert(b.BlameNodes, HasLen, 4)
	b.AddBlameNodes(createNewNode("3"))
	c.Assert(b.BlameNodes, HasLen, 4)
	b.SetBlame("helloworld", nil, false, "round5")
	c.Assert(b.FailReason, Equals, "helloworld")
	c.Assert(b.Round, Equals, "round5")
}

func (BlameTestSuite) TestAlreadyBlame(c *C) {
	b := NewBlame("test", nil)
	c.Assert(b.AlreadyBlame(), Equals, false)

	b.AddBlameNodes(createNewNode("node1"))
	c.Assert(b.AlreadyBlame(), Equals, true)
}

func (BlameTestSuite) TestAccessorsAndSnapshot(c *C) {
	b := NewBlame("initial reason", []Node{createNewNode("node1")})

	c.Assert(b.GetFailReason(), Equals, "initial reason")
	c.Assert(b.BlameNodesCount(), Equals, 1)

	b.SetIsUnicast(true)
	b.SetRound("round1")
	snapshot := b.Snapshot()
	c.Assert(snapshot.FailReason, Equals, "initial reason")
	c.Assert(snapshot.IsUnicast, Equals, true)
	c.Assert(snapshot.Round, Equals, "round1")
	c.Assert(snapshot.BlameNodes, HasLen, 1)
	c.Assert(snapshot.blameLock == b.blameLock, Equals, false)

	b.SetBlame("updated reason", nil, false, "round2")
	b.AddBlameNodes(createNewNode("node2"))
	c.Assert(snapshot.FailReason, Equals, "initial reason")
	c.Assert(snapshot.IsUnicast, Equals, true)
	c.Assert(snapshot.Round, Equals, "round1")
	c.Assert(snapshot.BlameNodes, HasLen, 1)

	snapshot.BlameNodes[0].Pubkey = "snapshot-only"
	current := b.Snapshot()
	c.Assert(current.BlameNodes[0].Pubkey, Equals, "node1")
	c.Assert(current.BlameNodes, HasLen, 2)
}

func (BlameTestSuite) TestConcurrentAccessorsAndSnapshot(c *C) {
	b := NewBlame("initial reason", nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			b.SetBlame(fmt.Sprintf("reason-%d", i), nil, i%2 == 0, fmt.Sprintf("round-%d", i))
			b.AddBlameNodes(createNewNode(fmt.Sprintf("node-%d", i)))
			b.SetIsUnicast(i%2 != 0)
			b.SetRound(fmt.Sprintf("updated-round-%d", i))
		}
	}()

	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			_ = b.GetFailReason()
			_ = b.BlameNodesCount()
			_ = b.Snapshot()
		}
	}()

	close(start)
	wg.Wait()
	c.Assert(b.BlameNodesCount(), Equals, 100)
}

func (BlameTestSuite) TestNodeEqual(c *C) {
	n1 := NewNode("pk1", []byte("data1"), []byte("sig1"))
	n2 := NewNode("pk1", []byte("data2"), []byte("sig1"))
	n3 := NewNode("pk2", []byte("data1"), []byte("sig1"))

	c.Assert(n1.Equal(n2), Equals, true)
	c.Assert(n1.Equal(n3), Equals, false)
}

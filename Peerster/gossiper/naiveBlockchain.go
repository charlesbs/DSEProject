package gossiper

import (
	"github.com/LiangweiCHEN/Peerster/message"
	"math/rand"
	"encoding/binary"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
	"fmt"

)

type Blockchain struct {

	// Blocks
	Blocks []*message.Block

	// Number of Peers
	N int

	// Next index of block to be added
	NextId int

	// Buffer of ballot to be added into blockchain
	Buffer []*message.CastBallot

	// Input channel for buffer
	InputCh chan *message.CastBallot

	// Send channel for candidate blocks
	SendCh chan *message.Block

	// Receive channel for candidate blocks
	ReceiveCh chan *message.Block

	// Mutexes for concurrent access of channels and buffers
	BufferMux sync.Mutex

	// Origin
	Origin string

	// Round map
	Map map[string]map[int]bool
	MapMux sync.Mutex
}


func Rand() uint64 {
	buf := make([]byte, 8)
	rand.Read(buf) // Always succeeds, no need to check error
	return binary.LittleEndian.Uint64(buf)
}

func (g *Gossiper) NewBlockchain() (bc *Blockchain) {
	/*
	This func create an instance of blockchain with genesis block
	*/
	// Create the channel
	bc = &Blockchain{
		Blocks : make([]*message.Block, 0),
		NextId : 0,
		Buffer : make([]*message.CastBallot, 0),
		InputCh : make(chan *message.CastBallot, 0),
		SendCh : make(chan *message.Block, 0),
		ReceiveCh : make(chan *message.Block, 0),
		N : g.NumPeers,
		Origin : g.Name,
		Map : make(map[string]map[int]bool),
	}

	// Add genesis block
	genesisBlock := &message.Block{
		PrevHash : sha256.Sum256(make([]byte, 0)),
		CurrentHash : sha256.Sum256(make([]byte, 0)),
		CastBallot : nil,
	}
	bc.Blocks = append(bc.Blocks, genesisBlock)
	bc.NextId = 1

	// Set random seed
	seedHash := sha256.Sum256([]byte(g.Name))
	seed := binary.BigEndian.Uint64(seedHash[:])
	rand.Seed(int64(seed))
	// Start working
	go bc.HandleRound()
	return
}

func (bc *Blockchain) CheckBlockValidty(b *message.Block) (bool) {
	return true
}

func (bc *Blockchain) HandleRound() {
	/*
	This function handle rounds of adding blocks into the blockchain
	*/
	voters := make(map[string]bool)
	for {
		bc.BufferMux.Lock()
		if len(bc.Buffer) > 0 {
			// Get the first Vote that has not been recorded in the blockchain to propagate
			var currentVote *message.CastBallot
			nextBufferIndex := 0
			for _, currentVote = range bc.Buffer{
				valid := true
				for _, b := range bc.Blocks {
					if b.CastBallot == nil {
						// Continue comparison if b is genesis block
						continue
					}
					if currentVote.VoterUuid == b.CastBallot.VoterUuid {
						valid = false
						break
					}
				}
				if valid {
					break
				} else {
					nextBufferIndex += 1
				}
			}
			if nextBufferIndex > len(bc.Buffer) {
				bc.Buffer = bc.Buffer[0: 0]
			} else {
				bc.Buffer = bc.Buffer[nextBufferIndex : ]
			}
			bc.BufferMux.Unlock()

			// Create the block
			currentBlock := &message.Block{
				CastBallot : currentVote,
				PrevHash : bc.Blocks[len(bc.Blocks) - 1].CurrentHash,
				Fitness : Rand(),
				Round : bc.NextId,
				Origin : bc.Origin,
			}
			currentBlock.CurrentHash = currentBlock.Hash()

			// Ask the gossiper to send the block
			bc.SendCh<- currentBlock

			// Wait for all peers' proposals
			count := 1
			fmt.Printf("OUR FITNESS IS %d\n", currentBlock.Fitness)
			receivedMap := make(map[string]bool)
			for {
				// Check validity of proposal, here we actually don't need to as 
				// all peers are trusted
				// Update self's block if peer's block has higher fitness value
				peerBlock := <-bc.ReceiveCh
				if _, ok := receivedMap[peerBlock.Origin]; !ok {
					receivedMap[peerBlock.Origin] = true
				} else {
					continue
				}
				fmt.Printf("Peer fitness is %d\n", peerBlock.Fitness)
				if bc.CheckBlockValidty(peerBlock) && peerBlock.Fitness > currentBlock.Fitness {
					currentBlock = peerBlock
				}
				count += 1
				fmt.Printf("RECEIVED %d proposals\n", count)
				if count == bc.N {
					break
				}
			}
			// Add the consensus block to the blockchain
			if _, ok := voters[currentBlock.CastBallot.VoterUuid]; ok {
				continue
			} else {
				voters[currentBlock.CastBallot.VoterUuid] = true
			}
			bc.Blocks = append(bc.Blocks, currentBlock)
			fmt.Printf("    APPENDING BLOCK WITH VOTER UID %s, VOTE HASH %s\n", currentBlock.CastBallot.VoterUuid,
						currentBlock.CastBallot.VoteHash)
			bc.NextId += 1
			fmt.Printf("ENTERING ROUND %d\n", bc.NextId)
		} else {
			bc.BufferMux.Unlock()
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func (bc *Blockchain) CreateBallot(voterid, vote string) (v *message.CastBallot) {
	/* This function create a ballot from the voterid and vote */

	voteHash := sha256.Sum256([]byte(vote))
	voterHash := sha256.Sum256([]byte(voterid))
	voteHashStr := hex.EncodeToString(voteHash[:])
	voterHashStr := hex.EncodeToString(voterHash[:])
	v = &message.CastBallot{
		VoteHash: voteHashStr,
		VoterHash: voterHashStr,
		VoterUuid: voterid,
	}

	return
}
func (g *Gossiper) HandleSendingBlocks() {
	/*
	This func receives blocks from underlying blockchain layer
	and send it using gossiper's rumor mongering
	*/

	for block := range g.Blockchain.SendCh {

		// Construct msg to be sent
		g.RumorBuffer.Mux.Lock()
		wrappedMessage := &message.WrappedRumorTLCMessage{
			BlockRumorMessage: &message.BlockRumorMessage{
				Origin: g.Name,
				ID:  uint32(len(g.RumorBuffer.Rumors[g.Name]) + 1),
				Block: block,
			},
		}

		// Store msg
		g.RumorBuffer.Rumors[g.Name] = append(g.RumorBuffer.Rumors[g.Name], wrappedMessage)

		// Update status
		g.StatusBuffer.Mux.Lock()

		if _, ok := g.StatusBuffer.Status[g.Name]; !ok {

			g.StatusBuffer.Status[g.Name] = 2
		} else {

			g.StatusBuffer.Status[g.Name] += 1
		}
		g.StatusBuffer.Mux.Unlock()
		g.RumorBuffer.Mux.Unlock()

		// Monger block
		fmt.Printf("PROPOSING BLOCK WITH VOTER %s VOTE %s", block.CastBallot.VoterUuid, block.CastBallot.VoteHash)
		g.MongerRumor(wrappedMessage, "", []string{})
	}
}

func (g *Gossiper) HandleReceivingBlock(wrapped_pkt *message.PacketIncome) {
	/*
	This func receive blocks from communication layer
	and inform blockchain layer
	Step 1. Add the vote to the blockchain buffer if it is empty
	Step 2. Inform the blockchain of the vote
	*/ 

	sender, blockRumor := wrapped_pkt.Sender, wrapped_pkt.Packet.BlockRumorMessage

	/* Step 1 */
	updated := g.Update(&message.WrappedRumorTLCMessage{
		BlockRumorMessage : blockRumor,
	}, sender)
	/*
	defer g.N.Send(&message.GossipPacket{
		Status: g.StatusBuffer.ToStatusPacket(),
	}, sender)
	*/
	// Check updated locally
	
	peerMsgID := int(blockRumor.ID)
	peerOrigin := blockRumor.Origin
	g.Blockchain.MapMux.Lock()
	if _, ok := g.Blockchain.Map[peerOrigin]; !ok {
		g.Blockchain.Map[peerOrigin] = make(map[int]bool)
	}
	if _, ok := g.Blockchain.Map[peerOrigin][peerMsgID]; !ok {
		g.Blockchain.Map[peerOrigin][peerMsgID] = true
	} else {
		// Having received before
		g.Blockchain.MapMux.Unlock()
		return
	}
	g.Blockchain.MapMux.Unlock()

	if updated{

		b := blockRumor.Block
		fmt.Printf("RECEVING BLOCK VOTER %s VOTE %s\n", b.CastBallot.VoterUuid, b.CastBallot.VoteHash)
		// Step 1
		g.Blockchain.BufferMux.Lock()
		if (len(g.Blockchain.Buffer) == 0) {
			g.Blockchain.Buffer = append(g.Blockchain.Buffer, b.CastBallot)
		}
		g.Blockchain.BufferMux.Unlock()
	
		// Step 2
		fmt.Println("Into receive channel")
		g.Blockchain.ReceiveCh<- b
		fmt.Println("Return from receive channel")

		/* Step 4 */
		wrappedMessage := &message.WrappedRumorTLCMessage{
			BlockRumorMessage: blockRumor,
		}
		g.MongerRumor(wrappedMessage, "", []string{sender})
	}

	return
}

func (g *Gossiper) HandleReceivingVote(v *message.CastBallot) {
	/*
	This func add the castballot received into the buffer
	*/

	g.Blockchain.BufferMux.Lock()
	g.Blockchain.Buffer = append(g.Blockchain.Buffer, v)
	g.Blockchain.BufferMux.Unlock()
	return
}
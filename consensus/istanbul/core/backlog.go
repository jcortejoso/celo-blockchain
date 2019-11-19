// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"github.com/ethereum/go-ethereum/common/prque"
	"github.com/ethereum/go-ethereum/consensus/istanbul"
	"math/big"
	"sort"
)

var (
	// msgPriority is defined for calculating processing priority to speedup consensus
	// istanbul.MsgPreprepare > istanbul.MsgCommit > istanbul.MsgPrepare
	msgPriority = map[uint64]int{
		istanbul.MsgPreprepare: 1,
		istanbul.MsgCommit:     2,
		istanbul.MsgPrepare:    3,
	}

	// Do not accept messages for views more than this many sequences in the future.
	acceptMaxFutureSequence             = big.NewInt(10)
	acceptMaxFutureMsgsFromOneValidator = 1000
	acceptMaxFutureMessages             = 10 * 1000
	acceptMaxFutureMessagesPruneBatch   = 100
)

// checkMessage checks the message state
// return errInvalidMessage if the message is invalid
// return errFutureMessage if the message view is larger than current view
// return errOldMessage if the message view is smaller than current view
func (c *core) checkMessage(msgCode uint64, view *istanbul.View) error {
	if view == nil || view.Sequence == nil || view.Round == nil {
		return errInvalidMessage
	}

	// Never accept messages too far into the future
	if view.Sequence.Cmp(new(big.Int).Add(c.currentView().Sequence, acceptMaxFutureSequence)) > 0 {
		return errTooFarInTheFutureMessage
	}

	// Round change messages should be in the same sequence but be >= the desired round
	if msgCode == istanbul.MsgRoundChange {
		if view.Sequence.Cmp(c.currentView().Sequence) > 0 {
			return errFutureMessage
		} else if view.Round.Cmp(c.current.DesiredRound()) < 0 {
			return errOldMessage
		}
		return nil
	}

	if view.Cmp(c.currentView()) > 0 {
		return errFutureMessage
	}

	// Discard messages from previous views, unless they are commits from the previous sequence,
	// with the same round as what we wound up finalizing, as we would be able to include those
	// to create the ParentAggregatedSeal for our next proposal.
	if view.Cmp(c.currentView()) < 0 {
		if msgCode == istanbul.MsgCommit {

			lastSubject, err := c.backend.LastSubject()
			if err != nil {
				return err
			}
			if view.Cmp(lastSubject.View) == 0 {
				return nil
			}
		}
		return errOldMessage
	}

	// Round change messages are already let through.
	if c.state == StateWaitingForNewRound {
		return errFutureMessage
	}

	// StateAcceptRequest only accepts istanbul.MsgPreprepare
	// other messages are future messages
	if c.state == StateAcceptRequest {
		if msgCode > istanbul.MsgPreprepare {
			return errFutureMessage
		}
		return nil
	}

	// For states(StatePreprepared, StatePrepared, StateCommitted),
	// can accept all message types if processing with same view
	return nil
}

func (c *core) storeBacklog(msg *istanbul.Message, src istanbul.Validator) {
	logger := c.logger.New("from", msg.Address, "state", c.state, "func", "storeBacklog")
	if c.current != nil {
		logger = logger.New("cur_seq", c.current.Sequence(), "cur_round", c.current.Round())
	} else {
		logger = logger.New("cur_seq", 0, "cur_round", -1)
	}

	if msg.Address == c.Address() {
		logger.Warn("Backlog from self")
		return
	}

	var v *istanbul.View
	switch msg.Code {
	case istanbul.MsgPreprepare:
		var p *istanbul.Preprepare
		err := msg.Decode(&p)
		if err != nil {
			return
		}
		v = p.View
	case istanbul.MsgPrepare:
		fallthrough
	case istanbul.MsgCommit:
		var p *istanbul.Subject
		err := msg.Decode(&p)
		if err != nil {
			return
		}
		v = p.View
	case istanbul.MsgRoundChange:
		var p *istanbul.RoundChange
		err := msg.Decode(&p)
		if err != nil {
			return
		}
		v = p.View
	}

	logger.Trace("Store future message", "msg", msg)

	c.backlogsMu.Lock()
	defer c.backlogsMu.Unlock()

	// Check and inc per-validator future message limit
	if c.backlogCountByVal[msg.Address] > acceptMaxFutureMsgsFromOneValidator {
		logger.Trace("Dropping: backlog exceeds per-src cap", "src", src)
		return
	}
	c.backlogCountByVal[src.Address()]++
	c.backlogTotal++

	// Add message to per-seq list
	backlogForSeq := c.backlogBySeq[v.Sequence.Uint64()]
	if backlogForSeq == nil {
		backlogForSeq = prque.New(nil)
		c.backlogBySeq[v.Sequence.Uint64()] = backlogForSeq
	}

	backlogForSeq.Push(msg, toPriority(msg.Code, v))

	// Keep backlog below total max size by pruning future-most sequence first
	// (we always leave one sequence's entire messages and rely on per-validator limits)
	if c.backlogTotal > acceptMaxFutureMessages {
		backlogSeqs := c.getSortedBacklogSeqs()
		for i := len(backlogSeqs) - 1; i > 0; i-- {
			seq := backlogSeqs[i]
			if seq <= c.currentView().Sequence.Uint64() ||
				c.backlogTotal < (acceptMaxFutureMessages-acceptMaxFutureMessagesPruneBatch) {
				break
			}
			c.drainBacklogForSeq(seq, nil)
		}
	}
}

// Return slice of sequences present in backlog sorted in ascending order
// Call with backlogsMu held.
func (c *core) getSortedBacklogSeqs() []uint64 {
	backlogSeqs := make([]uint64, len(c.backlogBySeq))
	i := 0
	for k := range c.backlogBySeq {
		backlogSeqs[i] = k
		i++
	}
	sort.Slice(backlogSeqs, func(i, j int) bool {
		return backlogSeqs[i] < backlogSeqs[j]
	})
	return backlogSeqs
}

// Drain a backlog for a given sequence, passing each to optional callback.
// Call with backlogsMu held.
func (c *core) drainBacklogForSeq(seq uint64, cb func(*istanbul.Message, istanbul.Validator)) {
	backlogForSeq := c.backlogBySeq[seq]
	if backlogForSeq == nil {
		return
	}

	for !backlogForSeq.Empty() {
		m := backlogForSeq.PopItem()
		msg := m.(*istanbul.Message)
		if cb != nil {
			_, src := c.valSet.GetByAddress(msg.Address)
			if src != nil {
				cb(msg, src)
			}
		}
		c.backlogCountByVal[msg.Address]--
		c.backlogTotal--
	}
	delete(c.backlogBySeq, seq)
}

func (c *core) processBacklog() {

	c.backlogsMu.Lock()
	defer c.backlogsMu.Unlock()

	for _, seq := range c.getSortedBacklogSeqs() {

		logger := c.logger.New("state", c.state, "seq", seq)

		if seq < c.currentView().Sequence.Uint64() {
			// Earlier sequence. Prune all messages.
			c.drainBacklogForSeq(seq, nil)
		} else if seq == c.currentView().Sequence.Uint64() {
			// Current sequence. Process all in order.
			c.drainBacklogForSeq(seq, func(msg *istanbul.Message, src istanbul.Validator) {
				var view *istanbul.View
				switch msg.Code {
				case istanbul.MsgPreprepare:
					var m *istanbul.Preprepare
					err := msg.Decode(&m)
					if err == nil {
						view = m.View
					}
				case istanbul.MsgPrepare:
					fallthrough
				case istanbul.MsgCommit:
					var sub *istanbul.Subject
					err := msg.Decode(&sub)
					if err == nil {
						view = sub.View
					}
				case istanbul.MsgRoundChange:
					var rc *istanbul.RoundChange
					err := msg.Decode(&rc)
					if err == nil {
						view = rc.View
					}
				}
				if view == nil {
					logger.Debug("Nil view", "msg", msg)
					// continue
					return
				}
				err := c.checkMessage(msg.Code, view)
				if err != nil {
					if err == errFutureMessage {
						// TODO(asa): Why is this unexpected? It could be for a future round...
						logger.Warn("Unexpected future message!", "msg", msg)
						//backlog.Push(msg, prio)
					}
					logger.Trace("Skip the backlog event", "msg", msg, "err", err)
					return
				}
				logger.Trace("Post backlog event", "msg", msg)

				go c.sendEvent(backlogEvent{
					src: src,
					msg: msg,
				})
			})
		} else {
			// got on to future messages.
			return
		}
	}
}

func toPriority(msgCode uint64, view *istanbul.View) int64 {
	if msgCode == istanbul.MsgRoundChange {
		// msgRoundChange comes first
		return 0
	}
	// 10 * Round limits the range possible message codes to [0, 9]
	// FIXME: Check for integer overflow
	return -int64(view.Round.Uint64()*10 + uint64(msgPriority[msgCode]))
}

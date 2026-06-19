/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package jobqueue

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type serverSubscription struct {
	keys           map[string]struct{}
	repGroupStates map[string]JobState
	queue          chan *JobUpdate
	deliveryQueue  chan *JobUpdate
	done           chan struct{}
	repGroup       string
	stateChanges   bool
	once           sync.Once
	mu             sync.RWMutex
	repGroupDone   bool
}

func newServerSubscription(keys []string, repGroup string, repGroupKeys []string) *serverSubscription {
	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		keySet[key] = struct{}{}
	}

	repGroupStates := make(map[string]JobState, len(repGroupKeys))
	for _, key := range repGroupKeys {
		repGroupStates[key] = ""
	}

	sub := &serverSubscription{
		keys:           keySet,
		repGroupStates: repGroupStates,
		queue:          make(chan *JobUpdate, serverSubscriptionQueueSize),
		deliveryQueue:  make(chan *JobUpdate, serverSubscriptionDeliveryQueueSize(len(keySet), len(repGroupStates))),
		done:           make(chan struct{}),
		repGroup:       repGroup,
	}

	go sub.deliverQueuedUpdates()

	return sub
}

func newStatusServerSubscription() *serverSubscription {
	sub := newServerSubscription(nil, "", nil)
	sub.stateChanges = true

	return sub
}

func (s *serverSubscription) addKeys(keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, key := range keys {
		if key != "" {
			s.keys[key] = struct{}{}
		}
	}
}

func (s *serverSubscription) removeKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if key == "" {
		clear(s.keys)

		return
	}

	delete(s.keys, key)
}

func (s *serverSubscription) matchesKey(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.keys) == 0 {
		return false
	}

	_, exists := s.keys[key]

	return exists
}

func (s *serverSubscription) matchesRepGroup(repGroup string) bool {
	return s.repGroup != "" && s.repGroup == repGroup
}

func (s *serverSubscription) acceptsUpdate(update *JobUpdate) bool {
	if update == nil {
		return false
	}

	if update.Kind == JobUpdateStateChange {
		return s.stateChanges
	}

	return true
}

func (s *serverSubscription) enqueue(update *JobUpdate) bool {
	select {
	case <-s.done:
		return false
	default:
	}

	select {
	case s.queue <- update:
		return true
	case <-s.done:
		return false
	}
}

func (s *serverSubscription) deliver(update *JobUpdate) bool {
	select {
	case <-s.done:
		return false
	default:
	}

	select {
	case s.deliveryQueue <- update:
		return true
	case <-s.done:
		return false
	}
}

func (s *serverSubscription) deliverQueuedUpdates() {
	for {
		select {
		case update := <-s.deliveryQueue:
			if !s.enqueue(update) {
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *serverSubscription) wait(timeout time.Duration) ([]*JobUpdate, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var updates []*JobUpdate

	select {
	case update := <-s.queue:
		updates = append(updates, update)
	case <-s.done:
		return nil, false
	case <-timer.C:
		return nil, true
	}

	for {
		select {
		case update := <-s.queue:
			updates = append(updates, update)
		case <-s.done:
			return updates, false
		default:
			return updates, true
		}
	}
}

func (s *serverSubscription) close() {
	s.once.Do(func() {
		close(s.done)
	})
}

func (s *serverSubscription) rememberRepGroupKey(key string) {
	if key == "" || s.repGroup == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.repGroupStates[key]; !exists {
		s.repGroupStates[key] = ""
	}
}

func (s *serverSubscription) recordRepGroupCatchUp(records map[string]subscriptionCatchUpRecord) *JobUpdate {
	if s.repGroup == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for key, record := range records {
		s.repGroupStates[key] = record.state
	}

	return s.repGroupDoneUpdate()
}

func (s *serverSubscription) recordRepGroupUpdate(update *JobUpdate) *JobUpdate {
	if s.repGroup == "" || update.Key == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.repGroupStates[update.Key] = update.State

	return s.repGroupDoneUpdate()
}

func (s *serverSubscription) repGroupDoneUpdate() *JobUpdate {
	if s.repGroupDone || len(s.repGroupStates) == 0 {
		return nil
	}

	update := repGroupAggregateUpdate(s.repGroup, s.repGroupStates)
	if update == nil {
		return nil
	}

	s.repGroupDone = true

	return update
}

func repGroupAggregateUpdate(repGroup string, states map[string]JobState) *JobUpdate {
	keys := sortedRepGroupStateKeys(states)
	update := &JobUpdate{
		Kind:     JobUpdateRepGroupDone,
		RepGroup: repGroup,
		JobKeys:  keys,
		Total:    len(keys),
	}

	for _, key := range keys {
		if !addRepGroupAggregateState(update, states[key]) {
			return nil
		}
	}

	if update.Lost > 0 {
		return nil
	}

	return update
}

func (s *Server) storeClientSubscription(sub *serverSubscription) string {
	id := fmt.Sprintf("sub-%d", atomic.AddUint64(&s.nextSubscriptionID, 1))

	s.csmutex.Lock()
	defer s.csmutex.Unlock()

	if s.clientSubscriptions == nil {
		s.clientSubscriptions = make(map[string]*serverSubscription)
	}

	s.clientSubscriptions[id] = sub

	return id
}

func (s *Server) closeClientSubscriptions() {
	s.csmutex.Lock()

	subs := make([]*serverSubscription, 0, len(s.clientSubscriptions))
	for id, sub := range s.clientSubscriptions {
		subs = append(subs, sub)

		delete(s.clientSubscriptions, id)
	}

	s.csmutex.Unlock()

	for _, sub := range subs {
		sub.close()
	}
}

func (s *Server) clientSubscription(id string) (*serverSubscription, bool) {
	s.csmutex.RLock()
	defer s.csmutex.RUnlock()

	sub, exists := s.clientSubscriptions[id]

	return sub, exists
}

type repGroupSubscriptionUpdate struct {
	sub    *serverSubscription
	update *JobUpdate
}

func (s *Server) subscriptionUpdatesForJob(update *JobUpdate) ([]*serverSubscription, []repGroupSubscriptionUpdate) {
	s.csmutex.RLock()
	defer s.csmutex.RUnlock()

	keySubs := make([]*serverSubscription, 0, len(s.clientSubscriptions))
	repGroupUpdates := make([]repGroupSubscriptionUpdate, 0)

	for _, sub := range s.clientSubscriptions {
		if !sub.acceptsUpdate(update) {
			continue
		}

		if sub.matchesKey(update.Key) {
			keySubs = append(keySubs, sub)
		}

		if !sub.matchesRepGroup(update.RepGroup) {
			continue
		}

		repGroupUpdate := sub.recordRepGroupUpdate(update)
		if repGroupUpdate != nil {
			repGroupUpdates = append(repGroupUpdates, repGroupSubscriptionUpdate{sub: sub, update: repGroupUpdate})
		}
	}

	return keySubs, repGroupUpdates
}

func serverSubscriptionDeliveryQueueSize(keyCount, repGroupKeyCount int) int {
	return max(serverSubscriptionQueueSize, 2*keyCount+repGroupKeyCount+1)
}

func addRepGroupAggregateState(update *JobUpdate, state JobState) bool {
	update.JobStates = append(update.JobStates, state)

	switch state {
	case JobStateComplete:
		update.Complete++
	case JobStateBuried:
		update.Buried++
	case JobStateLost:
		update.Lost++
	default:
		return false
	}

	return true
}

func sortedRepGroupStateKeys(states map[string]JobState) []string {
	keys := make([]string, 0, len(states))
	for key := range states {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func (s *Server) registerClientSubscription(keys []string, repGroup string) (string, error) {
	if len(keys) == 0 && repGroup == "" {
		return "", errMissingSubscriptionScope
	}

	sub := newServerSubscription(keys, repGroup, s.repGroupSubscriptionKeys(repGroup))

	return s.storeClientSubscription(sub), nil
}

func (s *Server) registerStatusSubscription() string {
	return s.storeClientSubscription(newStatusServerSubscription())
}

func (s *Server) repGroupSubscriptionKeys(repGroup string) []string {
	if repGroup == "" {
		return nil
	}

	return s.rpl.Values(repGroup)
}

func (s *Server) unregisterClientSubscription(id string) {
	s.csmutex.Lock()
	sub := s.clientSubscriptions[id]
	delete(s.clientSubscriptions, id)
	s.csmutex.Unlock()

	if sub != nil {
		sub.close()
	}
}

func (s *Server) waitForSubscriptionUpdates(id string, timeout time.Duration) ([]*JobUpdate, error) {
	sub, exists := s.clientSubscription(id)
	if !exists {
		return nil, errUnknownSubscription
	}

	if timeout <= 0 || timeout > serverSubscriptionHoldTime {
		timeout = serverSubscriptionHoldTime
	}

	updates, ok := sub.wait(timeout)
	if !ok {
		return nil, errSubscriptionClosed
	}

	return updates, nil
}

func (s *Server) seedRepGroupSubscription(id string, records map[string]subscriptionCatchUpRecord) *JobUpdate {
	sub, exists := s.clientSubscription(id)
	if !exists {
		return nil
	}

	return sub.recordRepGroupCatchUp(records)
}

func (s *Server) enqueueSubscriptionUpdate(update *JobUpdate) {
	keySubs, repGroupUpdates := s.subscriptionUpdatesForJob(update)
	deliveries := make([]repGroupSubscriptionUpdate, 0, len(keySubs)+len(repGroupUpdates))

	for _, sub := range keySubs {
		deliveries = append(deliveries, repGroupSubscriptionUpdate{sub: sub, update: update})
	}

	deliveries = append(deliveries, repGroupUpdates...)

	s.enqueueSubscriptionDeliveries(deliveries)
}

func (s *Server) enqueueSubscriptionDeliveries(deliveries []repGroupSubscriptionUpdate) {
	for _, delivery := range deliveries {
		delivery.sub.deliver(delivery.update)
	}
}

func (s *Server) hasClientSubscriptionsForJobUpdate(key, repGroup string, state JobState) bool {
	s.csmutex.RLock()
	defer s.csmutex.RUnlock()

	update := &JobUpdate{
		Kind:     jobUpdateKind(state),
		Key:      key,
		RepGroup: repGroup,
		State:    state,
	}

	for _, sub := range s.clientSubscriptions {
		if !sub.acceptsUpdate(update) {
			continue
		}

		if sub.matchesKey(key) || sub.matchesRepGroup(repGroup) {
			return true
		}
	}

	return false
}

func (s *Server) rememberRepGroupSubscriptionKey(repGroup, key string) {
	if repGroup == "" || key == "" {
		return
	}

	s.csmutex.RLock()
	defer s.csmutex.RUnlock()

	for _, sub := range s.clientSubscriptions {
		if sub.matchesRepGroup(repGroup) {
			sub.rememberRepGroupKey(key)
		}
	}
}

package watchable

import (
	"context"
	"errors"
	"sync"
)

// Update describes a mutation made to a Map.
type Update[K comparable, V any] struct {
	Key    K
	Delete bool // Whether this is deleting the entry for .Key, or setting it to .Value.
	Value  V
}

// Snapshot contains a snapshot of the current state of a Map, as well as a list of
// changes that have happened since the last snapshot.
type Snapshot[K comparable, V any] struct {
	// State is the current state of the snapshot.
	State map[K]V
	// Updates is the list of mutations that have happened since the previous snapshot.
	// Mutations that delete a value have .Delete=true, and .Value set to the value that was
	// deleted.  No-op updates are not included (i.e., setting something to its current value,
	// or deleting something that does not exist).
	Updates []Update[K, V]
}

// Map[K,V] is a wrapper around map[K]V that is very similar to sync.Map, and that provides the
// additional features that:
//
// 1. it is thread-safe (compared to a bare map)
// 2. it provides type safety (compared to a sync.Map)
// 3. it provides a compare-and-swap operation
// 4. you can Subscribe to either the whole map or just a subset of the map to watch for updates.
//    This gives you complete snapshots, deltas, and coalescing of rapid updates.
//
// Despite the type parameter for 'V' being 'any', it is not permissible to use any old type; the
// type must behave correctly in this package's 'DeepCopy' and 'DeepEqual' functions.  The type
// parameter is overly-permissive due to limitations in Go's type system.  See the documentation on
// those functions for more information.
type Map[K comparable, V any] struct {
	lock sync.RWMutex
	// things guarded by 'lock'
	close       chan struct{} // can read from the channel while unlocked, IF you've already validated it's non-nil
	value       map[K]V
	subscribers map[<-chan Update[K, V]]chan<- Update[K, V] // readEnd ↦ writeEnd

	// not guarded by 'lock'
	wg sync.WaitGroup
}

func (tm *Map[K, V]) unlockedInit() {
	if tm.close == nil {
		tm.close = make(chan struct{})
		tm.value = make(map[K]V)
		tm.subscribers = make(map[<-chan Update[K, V]]chan<- Update[K, V])
	}
}

func (tm *Map[K, V]) unlockedIsClosed() bool {
	select {
	case <-tm.close:
		return true
	default:
		return false
	}
}

func (tm *Map[K, V]) unlockedLoadAllMatching(includep func(K, V) bool) map[K]V {
	ret := make(map[K]V, len(tm.value))
	for k, v := range tm.value {
		if includep(k, v) {
			ret[k] = DeepCopy(v)
		}
	}
	return ret
}

// LoadAll returns a deepcopy of all key/value pairs in the map.
func (tm *Map[K, V]) LoadAll() map[K]V {
	return tm.LoadAllMatching(func(K, V) bool {
		return true
	})
}

// LoadAllMatching returns a deepcopy of all key/value pairs in the map for which the given function
// returns true.  The map is locked during the evaluation of the filter.
func (tm *Map[K, V]) LoadAllMatching(include func(K, V) bool) map[K]V {
	tm.lock.RLock()
	defer tm.lock.RUnlock()
	return tm.unlockedLoadAllMatching(include)
}

// Len returns the number of key/value pairs in the map.
func (tm *Map[K, V]) Len() int {
	tm.lock.RLock()
	defer tm.lock.RUnlock()
	return len(tm.value)
}

// Load returns a deepcopy of the value for a specific key.
func (tm *Map[K, V]) Load(key K) (value V, ok bool) {
	tm.lock.RLock()
	defer tm.lock.RUnlock()
	ret, ok := tm.value[key]
	if !ok {
		return ret, false
	}
	return DeepCopy(ret), true
}

// Store sets a key sets the value for a key.  This panics if .Close() has already been called.
func (tm *Map[K, V]) Store(key K, val V) {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	tm.unlockedStore(key, val)
}

// LoadOrStore returns the existing value for the key if present.  Otherwise, it stores and returns
// the given value. The 'loaded' result is true if the value was loaded, false if stored.
//
// If the value does need to be stored, all the same semantics as .Store() apply.
func (tm *Map[K, V]) LoadOrStore(key K, val V) (value V, loaded bool) {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	loadedVal, loadedOK := tm.value[key]
	if loadedOK {
		return DeepCopy(loadedVal), true
	}
	tm.unlockedStore(key, val)
	return DeepCopy(val), false
}

// CompareAndSwap is the atomic equivalent of:
//
//     if loadedVal, loadedOK := m.Load(key); loadedOK && loadedVal.Equal(old) {
//         m.Store(key, new)
//         return true
//     }
//     return false
func (tm *Map[K, V]) CompareAndSwap(key K, old, new V) bool {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	if loadedVal, loadedOK := tm.value[key]; loadedOK && DeepEqual(loadedVal, old) {
		tm.unlockedStore(key, new)
		return true
	}
	return false
}

func (tm *Map[K, V]) unlockedStore(key K, val V) {
	tm.unlockedInit()
	if tm.unlockedIsClosed() {
		panic(errors.New("watchable.Map: Store called on closed map"))
	}

	tm.value[key] = DeepCopy(val)
	for _, subscriber := range tm.subscribers {
		subscriber <- Update[K, V]{
			Key:   key,
			Value: DeepCopy(val),
		}
	}
}

// Delete deletes the value for a key.  This panics if .Close() has already been called.
func (tm *Map[K, V]) Delete(key K) {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	tm.unlockedDelete(key)
}

func (tm *Map[K, V]) unlockedDelete(key K) {
	tm.unlockedInit()
	if tm.unlockedIsClosed() {
		panic(errors.New("watchable.Map: Delete called on closed map"))
	}

	delete(tm.value, key)
	for _, subscriber := range tm.subscribers {
		subscriber <- Update[K, V]{
			Key:    key,
			Delete: true,
		}
	}
}

// LoadAndDelete deletes the value for a key, returning a deepcopy of the previous value if any.
// The 'loaded' result reports whether the key was present.
//
// If the value does need to be deleted, all the same semantics as .Delete() apply.
func (tm *Map[K, V]) LoadAndDelete(key K) (value V, loaded bool) {
	tm.lock.Lock()
	defer tm.lock.Unlock()

	loadedVal, loadedOK := tm.value[key]
	if !loadedOK {
		return loadedVal, false
	}

	tm.unlockedDelete(key)

	return DeepCopy(loadedVal), true
}

// Close marks the map as "finished", all subscriber channels are closed and further mutations are
// forbidden.
//
// After .Close() is called:
//
// - any attempts to mutate the map (calls to .Store() or .Delete()) will panic;
// - any attempts to read the map (calls to .Load(), .LoadAll(), or .LoadAllMatching()) will
//   continue to work normally; and
// - any calls to .Subscribe() or .SubscribeSubset() will return an already-closed channel.
func (tm *Map[K, V]) Close() {
	tm.lock.Lock()

	tm.unlockedInit()
	if !tm.unlockedIsClosed() {
		close(tm.close)
	}
	tm.lock.Unlock()
	tm.wg.Wait()
}

// internalSubscribe returns a channel (that blocks on both ends), that is written to on each map
// update.  If the map is already Close()ed, then this returns nil.
func (tm *Map[K, V]) internalSubscribe(_ context.Context) (<-chan Update[K, V], map[K]V) {
	tm.lock.Lock()
	defer tm.lock.Unlock()
	tm.unlockedInit()

	ret := make(chan Update[K, V])
	if tm.unlockedIsClosed() {
		return nil, nil
	}
	tm.subscribers[ret] = ret
	return ret, tm.unlockedLoadAllMatching(func(K, V) bool {
		return true
	})
}

// Subscribe returns a channel that will emit a complete snapshot of the map immediately after the
// call to Subscribe(), and then whenever the map changes.  Updates are coalesced; if you do not
// need to worry about reading from the channel faster than you are able.  The snapshot will contain
// the full list of coalesced updates; the initial snapshot will contain 0 updates.  A read from the
// channel will block as long as there are no changes since the last read.
//
// The values in the snapshot are deepcopies of the actual values in the map, but values may be
// reused between snapshots; if you mutate a value in a snapshot, that mutation may erroneously
// persist in future snapshots.
//
// The returned channel will be closed when the Context is Done, or .Close() is called.  If .Close()
// has already been called, then an already-closed channel is returned.
func (tm *Map[K, V]) Subscribe(ctx context.Context) <-chan Snapshot[K, V] {
	return tm.SubscribeSubset(ctx, func(K, V) bool {
		return true
	})
}

// SubscribeSubset is like Subscribe, but the snapshot returned only includes entries that satisfy
// the 'include' predicate.  Mutations to entries that don't satisfy the predicate do not cause a
// new snapshot to be emitted.  If the value for a key changes from satisfying the predicate to not
// satisfying it, then this is treated as a delete operation, and a new snapshot is generated.
func (tm *Map[K, V]) SubscribeSubset(ctx context.Context, include func(K, V) bool) <-chan Snapshot[K, V] {
	upstream, initialSnapshot := tm.internalSubscribe(ctx)
	downstream := make(chan Snapshot[K, V])

	if upstream == nil {
		close(downstream)
		return downstream
	}

	tm.wg.Add(1)
	go tm.coalesce(ctx, include, upstream, downstream, initialSnapshot)

	return downstream
}

func (tm *Map[K, V]) coalesce(
	ctx context.Context,
	includep func(K, V) bool,
	upstream <-chan Update[K, V],
	downstream chan<- Snapshot[K, V],
	initialSnapshot map[K]V,
) {
	defer tm.wg.Done()
	defer close(downstream)

	var shutdown func()
	shutdown = func() {
		// Make this function an empty one after first run to prevent launching the
		// following goroutine multiple times.
		shutdown = func() {}
		// Do this asynchronously because getting the lock might block a .Store() that's
		// waiting on us to read from 'upstream'!  We don't need to worry about separately
		// waiting for this goroutine because we implicitly do that when we drain
		// 'upstream'.
		go func() {
			tm.lock.Lock()
			defer tm.lock.Unlock()
			close(tm.subscribers[upstream])
			delete(tm.subscribers, upstream)
		}()
	}

	// Cur is a snapshot of the current state all the map according to all MAPTYPEUpdates we've
	// received from 'upstream', with any entries removed that do not satisfy the predicate
	// 'includep'.
	cur := make(map[K]V)
	for k, v := range initialSnapshot {
		if includep(k, v) {
			cur[k] = v
		}
	}

	snapshot := Snapshot[K, V]{
		// snapshot.State is a copy of 'cur' that we send to the 'downstream' channel.  We
		// don't send 'cur' directly because we're necessarily in a separate goroutine from
		// the reader of 'downstream', and map gets/sets aren't thread-safe, so we'd risk
		// memory corruption with our updating of 'cur' and the reader's accessing of 'cur'.
		// snapshot.State gets set to 'nil' when we need to do a read before we can write to
		// 'downstream' again.
		State: make(map[K]V, len(cur)),

		Updates: nil,
	}
	for k, v := range cur {
		snapshot.State[k] = v
	}

	// applyUpdate applies an update to 'cur', and updates 'snapshot.State' as nescessary.
	applyUpdate := func(update Update[K, V]) {
		if update.Delete || !includep(update.Key, update.Value) {
			if old, haveOld := cur[update.Key]; haveOld {
				update.Delete = true
				update.Value = old
				snapshot.Updates = append(snapshot.Updates, update)
				delete(cur, update.Key)
				if snapshot.State != nil {
					delete(snapshot.State, update.Key)
				} else {
					snapshot.State = make(map[K]V, len(cur))
					for k, v := range cur {
						snapshot.State[k] = v
					}
				}
			}
		} else {
			if old, haveOld := cur[update.Key]; !haveOld || !DeepEqual(old, update.Value) {
				snapshot.Updates = append(snapshot.Updates, update)
				cur[update.Key] = update.Value
				if snapshot.State != nil {
					snapshot.State[update.Key] = update.Value
				} else {
					snapshot.State = make(map[K]V, len(cur))
					for k, v := range cur {
						snapshot.State[k] = v
					}
				}
			}
		}
	}

	// The following loop is reading both a tm.close channel and the ctx.Done() channel. When the
	// tm.close channel is closed, the Map as a whole has been closed, and when ctx.Done() is closed,
	// the subscription that started this call to coalesce has ended. If one of the channels close,
	// the loop must call shutdown() and then continue looping,  now in a way that never selects the
	// closed channel. The closed channel is therefore set to `nil` so that it blocks forever, which
	// in essence means that the only way out of the loop is to close the `upstream` channel. This
	// happens when the subscription ends.
	closeCh := tm.close
	doneCh := ctx.Done()
	for {
		if snapshot.State == nil {
			select {
			case <-doneCh:
				shutdown()
				doneCh = nil
			case <-closeCh:
				shutdown()
				closeCh = nil
			case update, readOK := <-upstream:
				if !readOK {
					return
				}
				applyUpdate(update)
			}
		} else {
			// Same as above, but with an additional "downstream <- snapshot" case.
			select {
			case <-doneCh:
				shutdown()
				doneCh = nil
			case <-closeCh:
				shutdown()
				closeCh = nil
			case update, readOK := <-upstream:
				if !readOK {
					return
				}
				applyUpdate(update)
			case downstream <- snapshot:
				snapshot = Snapshot[K, V]{}
			}
		}
	}
}

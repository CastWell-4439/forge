package coordinator

import (
	"container/list"
	"sync"
	"time"
)

// TimingWheel implements a hierarchical timing wheel for efficient
// delayed task scheduling. It provides O(1) add and O(1) per-tick
// processing, much better than heap-based timers for large numbers
// of delayed tasks.
//
// Architecture:
//
//	Level 0: tick=1ms,   size=1000 slots -> covers 0~1s
//	Level 1: tick=1s,    size=60 slots   -> covers 1s~1m
//	Level 2: tick=1m,    size=60 slots   -> covers 1m~1h
//	Level 3: tick=1h,    size=24 slots   -> covers 1h~24h
//
// When a timer in the overflow wheel fires, it cascades down to
// the appropriate lower-level wheel.
type TimingWheel struct {
	tick     time.Duration
	size     int
	current  int
	slots    []*list.List
	overflow *TimingWheel // nil for the highest level
	mu       sync.Mutex
	interval time.Duration // tick * size (total range of this wheel)
	ticker   *time.Ticker
	stopCh   chan struct{}
}

// timerEntry represents a single timer in the wheel.
type timerEntry struct {
	deadline time.Time
	callback func()
}

// NewTimingWheel creates a new timing wheel.
// tick is the resolution (smallest delay unit).
// size is the number of slots.
func NewTimingWheel(tick time.Duration, size int) *TimingWheel {
	tw := &TimingWheel{
		tick:     tick,
		size:     size,
		current:  0,
		slots:    make([]*list.List, size),
		interval: tick * time.Duration(size),
		stopCh:   make(chan struct{}),
	}
	for i := range tw.slots {
		tw.slots[i] = list.New()
	}
	return tw
}

// NewHierarchicalTimingWheel creates a 4-level hierarchical timing wheel
// suitable for delays from 1ms to 24h.
func NewHierarchicalTimingWheel() *TimingWheel {
	level3 := NewTimingWheel(time.Hour, 24)
	level2 := NewTimingWheel(time.Minute, 60)
	level2.overflow = level3
	level1 := NewTimingWheel(time.Second, 60)
	level1.overflow = level2
	level0 := NewTimingWheel(time.Millisecond, 1000)
	level0.overflow = level1
	return level0
}

// Add schedules a callback to run after the given delay.
func (tw *TimingWheel) Add(delay time.Duration, callback func()) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	entry := &timerEntry{
		deadline: time.Now().Add(delay),
		callback: callback,
	}

	tw.addEntryLocked(entry)
}

// addEntryLocked places an entry into the correct slot. Caller must hold tw.mu.
func (tw *TimingWheel) addEntryLocked(entry *timerEntry) {
	// Use remaining time to deadline (not original delay) to account for
	// the current pointer having advanced since the timer was created.
	remaining := time.Until(entry.deadline)
	if remaining <= 0 {
		// Already expired -- fire immediately.
		go entry.callback()
		return
	}

	ticks := int(remaining / tw.tick)
	if ticks == 0 {
		ticks = 1 // at least next tick
	}

	if ticks < tw.size {
		slot := (tw.current + ticks) % tw.size
		tw.slots[slot].PushBack(entry)
	} else if tw.overflow != nil {
		tw.overflow.mu.Lock()
		tw.overflow.addEntryLocked(entry)
		tw.overflow.mu.Unlock()
	} else {
		// No overflow wheel -- put in the last slot (best effort).
		slot := (tw.current + tw.size - 1) % tw.size
		tw.slots[slot].PushBack(entry)
	}
}

// Start begins the timing wheel's tick loop.
func (tw *TimingWheel) Start() {
	tw.ticker = time.NewTicker(tw.tick)
	go tw.run()
}

// Stop stops the timing wheel.
func (tw *TimingWheel) Stop() {
	close(tw.stopCh)
	if tw.ticker != nil {
		tw.ticker.Stop()
	}
}

func (tw *TimingWheel) run() {
	for {
		select {
		case <-tw.stopCh:
			return
		case <-tw.ticker.C:
			tw.advance()
		}
	}
}

// advance moves the current pointer forward by one tick and fires expired timers.
func (tw *TimingWheel) advance() {
	tw.mu.Lock()
	tw.current = (tw.current + 1) % tw.size
	currentSlotIdx := tw.current
	slot := tw.slots[currentSlotIdx]

	var toFire []func()
	var toCascade []*timerEntry

	now := time.Now()
	for e := slot.Front(); e != nil; {
		next := e.Next()
		entry := e.Value.(*timerEntry)

		if now.Before(entry.deadline) {
			// Re-insert: addEntryLocked will route to correct wheel based on remaining time.
			toCascade = append(toCascade, &timerEntry{
				deadline: entry.deadline,
				callback: entry.callback,
			})
		} else {
			toFire = append(toFire, entry.callback)
		}

		slot.Remove(e)
		e = next
	}

	needCascade := currentSlotIdx == 0 && tw.overflow != nil
	tw.mu.Unlock()

	for _, entry := range toCascade {
		tw.mu.Lock()
		tw.addEntryLocked(entry)
		tw.mu.Unlock()
	}

	for _, cb := range toFire {
		go cb()
	}

	if needCascade {
		tw.cascadeFromOverflow()
	}
}

// cascadeFromOverflow pulls timers from the next overflow slot down to this wheel.
func (tw *TimingWheel) cascadeFromOverflow() {
	tw.overflow.mu.Lock()
	tw.overflow.current = (tw.overflow.current + 1) % tw.overflow.size
	slot := tw.overflow.slots[tw.overflow.current]

	var entries []*timerEntry
	for e := slot.Front(); e != nil; {
		next := e.Next()
		entry := e.Value.(*timerEntry)
		entries = append(entries, entry)
		slot.Remove(e)
		e = next
	}
	tw.overflow.mu.Unlock()

	now := time.Now()
	tw.mu.Lock()
	for _, entry := range entries {
		remaining := entry.deadline.Sub(now)
		if remaining <= 0 {
			go entry.callback()
		} else {
			tw.addEntryLocked(&timerEntry{
				deadline: entry.deadline,
				callback: entry.callback,
			})
		}
	}
	tw.mu.Unlock()
}

// Pending returns the total number of pending timers across all levels.
func (tw *TimingWheel) Pending() int {
	tw.mu.Lock()
	count := 0
	for _, slot := range tw.slots {
		count += slot.Len()
	}
	tw.mu.Unlock()

	if tw.overflow != nil {
		count += tw.overflow.Pending()
	}
	return count
}

package rt

// Detailed STANDARD opt-in counters. State contains only static type metadata,
// anonymous digests and numeric site shadows; never messages/models/frames.
import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	spaDetailVersion             = 1
	spaDetailMaxMessages         = 1024
	spaDetailMaxSites            = 4096
	spaDetailMaxPending          = 128
	spaDetailMaxTypeBytes        = 1024
	spaDetailMaxCounter   uint64 = 1<<53 - 1
)

type spaDetailStamp struct {
	boot, epoch uint64
	cache       uint32
}
type spaDetailCache struct {
	boot uint64
	id   uint32
}
type spaDetailFrame struct {
	stamp  spaDetailStamp
	counts map[int]uint64
	known  bool
}
type spaDetailSite struct {
	calls, builds, hits, admissions, commits, resets, lastInputRows uint64
	lastInputRowsKnown                                              bool
}
type spaDetailEvent struct {
	reset  bool
	counts map[int]uint64
}
type spaDetailState struct {
	boot, epoch                      uint64
	cacheAssigned, multipleCaches    bool
	messages                         map[string]uint64
	typeKeys                         map[reflect.Type]string
	keyTypes                         map[string]reflect.Type
	sites                            map[int]*spaDetailSite
	gauge                            map[int]uint64 // immutable numeric shadow of the ACTUAL current map
	gaugeKnown                       bool
	pending                          []spaDetailEvent // fixed cap; only immutable numeric shadows
	overflow, unsupported, collision bool
}

var spaDetailEnabled atomic.Bool
var spaDetailMu sync.Mutex
var spaDetailStateNow *spaDetailState
var spaDetailGeneration uint64

func spaProfileDetailStart(enabled bool) {
	if !enabled && !spaDetailEnabled.Load() {
		return
	}
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	if !enabled {
		spaDetailEnabled.Store(false)
		spaDetailStateNow = nil
		return
	}
	exhausted := spaDetailGeneration >= spaDetailMaxCounter
	if !exhausted {
		spaDetailGeneration++
	}
	spaDetailStateNow = &spaDetailState{
		boot: spaDetailGeneration, epoch: spaDetailGeneration,
		messages: make(map[string]uint64), typeKeys: make(map[reflect.Type]string),
		keyTypes: make(map[string]reflect.Type), sites: make(map[int]*spaDetailSite),
		pending: make([]spaDetailEvent, 0, spaDetailMaxPending), overflow: exhausted,
	}
	spaDetailEnabled.Store(true)
}

// Counter reset is NOT an algorithm reset. Preserve the exact numeric live
// gauge and cache identity; no traversal/recovery of the actual cache occurs.
func spaProfileDetailResetCounters() {
	if !spaDetailEnabled.Load() {
		return
	}
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	s := spaDetailStateNow
	if s == nil {
		return
	}
	exhausted := spaDetailGeneration >= spaDetailMaxCounter
	if !exhausted {
		spaDetailGeneration++
	}
	s.epoch = spaDetailGeneration
	clear(s.messages)
	clear(s.sites)
	clear(s.pending)
	s.pending = s.pending[:0]
	s.overflow = exhausted
	s.unsupported = s.multipleCaches
	// A witnessed digest collision remains ambiguous for this boot.
}

func spaDetailBad() {
	if !spaDetailEnabled.Load() {
		return
	}
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	if spaDetailStateNow != nil {
		spaDetailStateNow.unsupported = true
	}
}
func spaDetailRecover() {
	if recover() != nil {
		spaDetailBad()
	}
}
func spaDetailAdd(s *spaDetailState, value *uint64, n uint64) {
	if *value > spaDetailMaxCounter || n > spaDetailMaxCounter-*value {
		*value = spaDetailMaxCounter
		s.overflow = true
	} else {
		*value += n
	}
}
func spaDetailSiteLocked(s *spaDetailState, site int) *spaDetailSite {
	if site < 0 || site >= spaDetailMaxSites {
		s.unsupported = true
		return nil
	}
	if found := s.sites[site]; found != nil {
		return found
	}
	if len(s.sites) >= spaDetailMaxSites {
		s.overflow = true
		return nil
	}
	value := &spaDetailSite{}
	s.sites[site] = value
	return value
}
func spaDetailStampLocked(s *spaDetailState, stamp spaDetailStamp) bool {
	if stamp.boot != s.boot || stamp.epoch != s.epoch || stamp.cache != 1 {
		s.unsupported = true
		return false
	}
	return true
}

// SHA256(PkgPath || NUL || Name), only a concrete nongeneric named sealed
// struct. Reflection METADATA only: never invoke SkyVariant/user methods.
func spaDetailMessageKey(t reflect.Type) (string, bool) {
	if t == nil || t.Kind() != reflect.Struct || t == reflect.TypeOf(SkyADT{}) {
		return "", false
	}
	name, pkg := t.Name(), t.PkgPath()
	if name == "" || pkg == "" || len(name) > spaDetailMaxTypeBytes || len(pkg) > spaDetailMaxTypeBytes || strings.ContainsAny(name, "[]") {
		return "", false
	}
	if !t.Implements(reflect.TypeOf((*SkyVariant)(nil)).Elem()) {
		return "", false
	}
	sum := sha256.Sum256([]byte(pkg + "\x00" + name))
	return hex.EncodeToString(sum[:]), true
}
func spaDetailRegisterTypeLocked(s *spaDetailState, t reflect.Type, key string) bool {
	if old, ok := s.typeKeys[t]; ok && old != key {
		s.collision = true
		return false
	}
	if old, ok := s.keyTypes[key]; ok && old != t {
		s.collision = true
		return false
	}
	if _, ok := s.typeKeys[t]; !ok && len(s.typeKeys) >= spaDetailMaxMessages {
		s.overflow = true
		return false
	}
	s.keyTypes[key] = t
	s.typeKeys[t] = key
	return true
}
func spaProfileDetailDispatch(msg any) {
	if !spaDetailEnabled.Load() {
		return
	}
	defer spaDetailRecover()
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	s := spaDetailStateNow
	if s == nil {
		return
	}
	t := reflect.TypeOf(msg)
	key, found := s.typeKeys[t]
	if !found {
		var ok bool
		key, ok = spaDetailMessageKey(t)
		if !ok {
			s.unsupported = true
			return
		}
		if !spaDetailRegisterTypeLocked(s, t, key) {
			return
		}
	}
	count := s.messages[key]
	spaDetailAdd(s, &count, 1)
	s.messages[key] = count
}

// Caller holds the existing cache mutex, or the cache is newly created and
// unpublished. No profile-lock -> algorithm-lock path exists.
func spaDetailCacheLocked(c *spaViewMemoCache, knownEmptyCreation bool) *spaDetailFrame {
	if !spaDetailEnabled.Load() {
		return nil
	}
	defer spaDetailRecover()
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	s := spaDetailStateNow
	if s == nil {
		return nil
	}
	if c.detail == nil || c.detail.boot != s.boot {
		if s.cacheAssigned {
			s.multipleCaches = true
			s.unsupported = true
			s.gaugeKnown = false
			return nil
		}
		s.cacheAssigned = true
		if c.detail == nil {
			c.detail = &spaDetailCache{}
		}
		c.detail.boot = s.boot
		c.detail.id = 1
		if knownEmptyCreation {
			s.gauge = nil
			s.gaugeKnown = true
		}
	}
	return &spaDetailFrame{stamp: spaDetailStamp{s.boot, s.epoch, c.detail.id}, known: true}
}
func spaProfileDetailNewCache(c *spaViewMemoCache) {
	if spaDetailEnabled.Load() {
		spaDetailCacheLocked(c, true)
	}
}
func spaDetailFrameStamp(f *spaViewMemoFrame) spaDetailStamp {
	if f != nil && f.detail != nil {
		return f.detail.stamp
	}
	return spaDetailStamp{}
}
func spaProfileDetailSite(f *spaViewMemoFrame, site int, kind uint8, rows reflect.Value) {
	if !spaDetailEnabled.Load() {
		return
	}
	defer spaDetailRecover()
	stamp := spaDetailFrameStamp(f)
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	s := spaDetailStateNow
	if s == nil || !spaDetailStampLocked(s, stamp) {
		return
	}
	v := spaDetailSiteLocked(s, site)
	if v == nil {
		return
	}
	switch kind {
	case 0:
		spaDetailAdd(s, &v.calls, 1)
	case 1:
		spaDetailAdd(s, &v.builds, 1)
	case 2:
		spaDetailAdd(s, &v.hits, 1)
	case 3:
		if !rows.IsValid() || rows.Kind() != reflect.Slice {
			s.unsupported = true
			return
		}
		n := rows.Len()
		if uint64(n) > spaDetailMaxCounter {
			s.overflow = true
			return
		}
		v.lastInputRows = uint64(n)
		v.lastInputRowsKnown = true
	}
}

// Called at the EXISTING next-map insertion, under frame.mu. The shadow has
// only site->count numbers. Its ownership is transferred on actual publication.
func spaProfileDetailAdmission(f *spaViewMemoFrame, site int) {
	if !spaDetailEnabled.Load() {
		if f.detail != nil {
			f.detail.known = false
		}
		return
	}
	defer func() {
		if recover() != nil {
			if f.detail != nil {
				f.detail.known = false
			}
			spaDetailBad()
		}
	}()
	if f.detail == nil {
		spaDetailBad()
		return
	}
	if site < 0 || site >= spaDetailMaxSites {
		f.detail.known = false
		spaDetailBad()
		return
	}
	if f.detail.counts == nil {
		f.detail.counts = make(map[int]uint64)
	}
	if (f.detail.counts[site] == 0 && len(f.detail.counts) >= spaViewMemoMaxEntries) || f.detail.counts[site] >= spaViewMemoMaxEntries {
		f.detail.known = false
		spaDetailOverflow()
		return
	}
	f.detail.counts[site]++ // bounded by the algorithm's existing 128 next entries
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	s := spaDetailStateNow
	if s == nil || !spaDetailStampLocked(s, f.detail.stamp) {
		return
	}
	if v := spaDetailSiteLocked(s, site); v != nil {
		spaDetailAdd(s, &v.admissions, 1)
	}
}
func spaDetailQueueLocked(s *spaDetailState, reset bool, counts map[int]uint64) {
	if len(counts) == 0 {
		return
	}
	if len(s.pending) >= spaDetailMaxPending {
		s.overflow = true
		return
	}
	s.pending = append(s.pending, spaDetailEvent{reset, counts})
}

// This never reads/traverses actual cache entries. Publication is O(1); the
// bounded numeric event is folded only at snapshot outside measured phases.
func spaProfileDetailPublish(frame *spaDetailFrame, empty bool) {
	if !spaDetailEnabled.Load() {
		return
	}
	defer spaDetailRecover()
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	s := spaDetailStateNow
	if s == nil {
		return
	}
	if s.multipleCaches {
		s.gaugeKnown = false
		return
	}
	if frame == nil || frame.stamp.boot != s.boot || frame.stamp.cache != 1 {
		s.unsupported = true
		s.gaugeKnown = false
		return
	}
	current := spaDetailStampLocked(s, frame.stamp)
	if !frame.known && !empty {
		s.gaugeKnown = false
		return
	}
	s.gauge = frame.counts
	s.gaugeKnown = true
	if current {
		spaDetailQueueLocked(s, false, frame.counts)
	}
}
func spaProfileDetailClear(stamp spaDetailStamp, empty bool) {
	if !spaDetailEnabled.Load() {
		return
	}
	defer spaDetailRecover()
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	s := spaDetailStateNow
	if s == nil {
		return
	}
	if s.multipleCaches {
		s.gaugeKnown = false
		return
	}
	if stamp.boot != s.boot || stamp.cache != 1 {
		s.unsupported = true
		s.gaugeKnown = false
		return
	}
	current := spaDetailStampLocked(s, stamp)
	if current {
		if s.gaugeKnown {
			spaDetailQueueLocked(s, true, s.gauge)
		} else if !empty {
			s.unsupported = true
		}
	}
	s.gauge = nil
	s.gaugeKnown = true
}
func spaDetailCurrentCacheStamp(c *spaViewMemoCache) spaDetailStamp {
	if !spaDetailEnabled.Load() {
		return spaDetailStamp{}
	}
	f := spaDetailCacheLocked(c, false)
	if f == nil {
		return spaDetailStamp{}
	}
	return f.stamp
}

type spaDetailSnapshot struct {
	version                                                int
	epoch                                                  uint64
	complete, overflow, unsupported, collision, gaugeKnown bool
	messages                                               map[string]uint64
	sites                                                  map[int]spaDetailSite
	gauge                                                  map[int]uint64
}

func spaProfileDetailSnapshot() (result spaDetailSnapshot) {
	if !spaDetailEnabled.Load() {
		return spaDetailSnapshot{version: spaDetailVersion, unsupported: true}
	}
	defer func() {
		if recover() != nil {
			spaDetailBad()
			result = spaDetailSnapshot{version: spaDetailVersion, unsupported: true}
		}
	}()
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	s := spaDetailStateNow
	if s == nil {
		return spaDetailSnapshot{version: spaDetailVersion, unsupported: true}
	}
	for _, event := range s.pending {
		for site, count := range event.counts {
			if v := spaDetailSiteLocked(s, site); v != nil {
				if event.reset {
					spaDetailAdd(s, &v.resets, count)
				} else {
					spaDetailAdd(s, &v.commits, count)
				}
			}
		}
	}
	clear(s.pending)
	s.pending = s.pending[:0]
	result = spaDetailSnapshot{version: spaDetailVersion, epoch: s.epoch, gaugeKnown: s.gaugeKnown,
		overflow: s.overflow, unsupported: s.unsupported, collision: s.collision,
		complete: s.gaugeKnown && !s.overflow && !s.unsupported && !s.collision,
		messages: make(map[string]uint64, len(s.messages)), sites: make(map[int]spaDetailSite, len(s.sites)), gauge: make(map[int]uint64, len(s.gauge))}
	for k, v := range s.messages {
		result.messages[k] = v
	}
	for k, v := range s.sites {
		result.sites[k] = *v
	}
	for k, v := range s.gauge {
		result.gauge[k] = v
		if _, ok := result.sites[k]; !ok {
			result.sites[k] = spaDetailSite{}
		}
	}
	return result
}
func (s spaDetailSnapshot) jsValue() map[string]any {
	messages := make(map[string]any, len(s.messages))
	for k, v := range s.messages {
		messages[k] = float64(v)
	}
	sites := make(map[string]any, len(s.sites))
	for k, v := range s.sites {
		var rows, entries any
		if v.lastInputRowsKnown {
			rows = float64(v.lastInputRows)
		}
		if s.gaugeKnown {
			entries = float64(s.gauge[k])
		}
		sites[strconv.Itoa(k)] = map[string]any{"calls": float64(v.calls), "builds": float64(v.builds), "hits": float64(v.hits),
			"admissions": float64(v.admissions), "commits": float64(v.commits), "resets": float64(v.resets),
			"lastInputRows": rows, "lastInputRowsKnown": v.lastInputRowsKnown, "committedEntries": entries}
	}
	return map[string]any{"version": s.version, "epoch": float64(s.epoch), "complete": s.complete, "overflow": s.overflow,
		"unsupported": s.unsupported, "collision": s.collision, "gaugeKnown": s.gaugeKnown, "messages": messages, "sites": sites}
}

func spaProfileDetailFrameEnd(frame *spaDetailFrame) {
	if !spaDetailEnabled.Load() {
		return
	}
	defer spaDetailRecover()
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	s := spaDetailStateNow
	if s == nil {
		return
	}
	if frame == nil {
		s.unsupported = true
		s.gaugeKnown = false
		return
	}
	spaDetailStampLocked(s, frame.stamp)
}

func spaDetailOverflow() {
	if !spaDetailEnabled.Load() {
		return
	}
	spaDetailMu.Lock()
	defer spaDetailMu.Unlock()
	if spaDetailStateNow != nil {
		spaDetailStateNow.overflow = true
	}
}

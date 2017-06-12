package prometheus

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/stats"
)

type metricType int

const (
	untyped metricType = iota
	counter
	gauge
	histogram
	summary
)

func metricTypeOf(t stats.MetricType) metricType {
	switch t {
	case stats.CounterType:
		return counter
	case stats.GaugeType:
		return gauge
	case stats.HistogramType:
		return histogram
	default:
		return untyped
	}
}

func (t metricType) String() string {
	switch t {
	case untyped:
		return "untyped"
	case counter:
		return "counter"
	case gauge:
		return "gauge"
	case histogram:
		return "histogram"
	case summary:
		return "summary"
	default:
		return "unknown"
	}
}

type metricKey struct {
	scope string
	name  string
}

type metric struct {
	mtype  metricType
	scope  string
	name   string
	help   string
	value  float64
	time   time.Time
	labels labels
}

func (m metric) key() metricKey {
	return metricKey{scope: m.scope, name: m.name}
}

func (m metric) rootName() string {
	if m.mtype == histogram {
		return m.name[:strings.LastIndexByte(m.name, '_')]
	}
	return m.name
}

type metricStore struct {
	mutex   sync.RWMutex
	entries map[metricKey]*metricEntry
}

func (store *metricStore) lookup(mtype metricType, key metricKey, help string) *metricEntry {
	store.mutex.RLock()
	entry := store.entries[key]
	store.mutex.RUnlock()

	// The program may choose to change the type of a metric, this is likely a
	// pretty bad idea but I don't think we have enough context here to tell if
	// it's a bug or a feature so we just accept to mutate the entry.
	if entry == nil || entry.mtype != mtype {
		store.mutex.Lock()

		if store.entries == nil {
			store.entries = make(map[metricKey]*metricEntry)
		}

		if entry = store.entries[key]; entry == nil || entry.mtype != mtype {
			entry = newMetricEntry(mtype, key.scope, key.name, help)
			store.entries[key] = entry
		}

		store.mutex.Unlock()
	}

	return entry
}

func (store *metricStore) update(metric metric, buckets []float64) {
	entry := store.lookup(metric.mtype, metric.key(), metric.help)
	state := entry.lookup(metric.labels)
	state.update(metric.mtype, metric.value, metric.time, buckets)
}

func (store *metricStore) collect(metrics []metric) []metric {
	store.mutex.RLock()

	for _, entry := range store.entries {
		metrics = entry.collect(metrics)
	}

	store.mutex.RUnlock()
	return metrics
}

func (store *metricStore) cleanup(exp time.Time) {
	store.mutex.RLock()

	for name, entry := range store.entries {
		store.mutex.RUnlock()

		entry.cleanup(exp, func() {
			store.mutex.Lock()
			delete(store.entries, name)
			store.mutex.Unlock()
		})

		store.mutex.RLock()
	}

	store.mutex.RUnlock()
}

type metricEntry struct {
	mutex  sync.RWMutex
	mtype  metricType
	scope  string
	name   string
	help   string
	bucket string
	sum    string
	count  string
	states metricStateMap
}

func newMetricEntry(mtype metricType, scope string, name string, help string) *metricEntry {
	entry := &metricEntry{
		mtype:  mtype,
		scope:  scope,
		name:   name,
		help:   help,
		states: make(metricStateMap),
	}

	if mtype == histogram {
		// Here we cache those metric names to avoid having to recompute them
		// every time we collect the state of the metrics.
		entry.bucket = name + "_bucket"
		entry.sum = name + "_sum"
		entry.count = name + "_count"
	}

	return entry
}

func (entry *metricEntry) lookup(labels labels) *metricState {
	key := labels.hash()

	entry.mutex.RLock()
	state := entry.states.find(key, labels)
	entry.mutex.RUnlock()

	if state == nil {
		entry.mutex.Lock()

		if state = entry.states.find(key, labels); state == nil {
			state = newMetricState(labels)
			entry.states.put(key, state)
		}

		entry.mutex.Unlock()
	}

	return state
}

func (entry *metricEntry) collect(metrics []metric) []metric {
	entry.mutex.RLock()

	if len(entry.states) != 0 {
		for _, states := range entry.states {
			for _, state := range states {
				metrics = state.collect(metrics, entry)
			}
		}
	}

	entry.mutex.RUnlock()
	return metrics
}

func (entry *metricEntry) cleanup(exp time.Time, empty func()) {
	// TODO: there may be high contention on this mutex, maybe not, it would be
	// a good idea to measure.
	entry.mutex.Lock()

	for hash, states := range entry.states {
		i := 0

		for j, state := range states {
			states[j] = nil

			// We expire all entries that have been last updated before exp,
			// they don't get copied back into the state slice.
			if exp.Before(state.time.load()) {
				states[i] = state
				i++
			}
		}

		if states = states[:i]; len(states) == 0 {
			delete(entry.states, hash)
		} else {
			entry.states[hash] = states
		}
	}

	if len(entry.states) == 0 {
		empty()
	}

	entry.mutex.Unlock()
}

type metricState struct {
	// immutable
	labels labels
	// mutable
	buckets metricBuckets
	value   atomicFloat64
	sum     atomicFloat64
	count   atomicFloat64
	time    atomicTime
}

func newMetricState(labels labels) *metricState {
	return &metricState{
		labels: labels.copy(),
	}
}

func (state *metricState) update(mtype metricType, value float64, time time.Time, buckets []float64) {
	switch mtype {
	case counter:
		state.value.add(value)

	case gauge:
		state.value.store(value)

	case histogram:
		if len(state.buckets) != len(buckets) {
			state.buckets = makeMetricBuckets(buckets, state.labels)
		}
		state.buckets.update(value)
		state.sum.add(value)
		state.count.add(1)
	}

	state.time.store(time)
}

func (state *metricState) collect(metrics []metric, entry *metricEntry) []metric {
	switch entry.mtype {
	case counter, gauge:
		metrics = append(metrics, metric{
			mtype:  entry.mtype,
			scope:  entry.scope,
			name:   entry.name,
			help:   entry.help,
			value:  state.value.load(),
			time:   state.time.load(),
			labels: state.labels,
		})

	case histogram:
		buckets := state.buckets
		time := state.time.load()

		for i := range buckets {
			metrics = append(metrics, metric{
				mtype:  entry.mtype,
				name:   entry.bucket,
				help:   entry.help,
				value:  float64(buckets[i].count.load()),
				time:   time,
				labels: buckets[i].labels,
			})
		}
		metrics = append(metrics,
			metric{
				mtype:  entry.mtype,
				name:   entry.sum,
				help:   entry.help,
				value:  state.sum.load(),
				time:   time,
				labels: state.labels,
			},
			metric{
				mtype:  entry.mtype,
				name:   entry.count,
				help:   entry.help,
				value:  float64(state.count.load()),
				time:   time,
				labels: state.labels,
			},
		)
	}

	return metrics
}

type metricStateMap map[uint64][]*metricState

func (m metricStateMap) put(key uint64, state *metricState) {
	m[key] = append(m[key], state)
}

func (m metricStateMap) find(key uint64, labels labels) *metricState {
	states := m[key]

	for _, state := range states {
		if state.labels.equal(labels) {
			return state
		}
	}

	return nil
}

type metricBucket struct {
	count  atomicUint64
	limit  float64
	labels labels
}

type metricBuckets []metricBucket

func makeMetricBuckets(buckets []float64, labels labels) metricBuckets {
	b := make(metricBuckets, len(buckets))
	for i := range buckets {
		b[i].limit = buckets[i]
		b[i].labels = labels.copyAppend(label{"le", ftoa(buckets[i])})
	}
	return b
}

func (m metricBuckets) update(value float64) {
	for i := range m {
		if value <= m[i].limit {
			m[i].count.add(1)
			break
		}
	}
}

func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

type byNameAndLabels []metric

func (metrics byNameAndLabels) Len() int {
	return len(metrics)
}

func (metrics byNameAndLabels) Swap(i int, j int) {
	metrics[i], metrics[j] = metrics[j], metrics[i]
}

func (metrics byNameAndLabels) Less(i int, j int) bool {
	m1 := &metrics[i]
	m2 := &metrics[j]
	return m1.name < m2.name || (m1.name == m2.name && m1.labels.less(m2.labels))
}

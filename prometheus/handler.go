package prometheus

import (
	"compress/gzip"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/segmentio/stats"
)

// Handler is a type that bridges the stats API to a prometheus-compatible HTTP
// endpoint.
//
// Typically, a program creates one Handler, registers it to the stats package,
// and adds it to the muxer used by the application under the /metrics path.
//
// The handle ignores histograms that have no buckets set.
type Handler struct {
	// MetricTimeout defines how long the handler exposes metrics that aren't
	// receiving updates.
	//
	// The default is to use a 2 minutes metric timeout.
	MetricTimeout time.Duration

	opcount uint64
	metrics metricStore
}

// HandleMetric satisfies the stats.Handler interface.
func (h *Handler) HandleMetric(m *stats.Metric) {
	mtime := m.Time
	if mtime.IsZero() {
		mtime = time.Now()
	}

	cache := handleMetricPool.Get().(*handleMetricCache)
	cache.labels = cache.labels.appendTags(m.Tags...)

	if !sort.IsSorted(cache) {
		sort.Sort(cache)
	}

	h.metrics.update(metric{
		mtype:  metricTypeOf(m.Type),
		scope:  m.Namespace,
		name:   m.Name,
		value:  m.Value,
		time:   mtime,
		labels: cache.labels,
	}, m.Buckets)

	cache.labels = cache.labels[:0]
	handleMetricPool.Put(cache)

	// Every 10K updates we cleanup the metric store of outdated entries to
	// having memory leaks if the program has generated metrics for a pair of
	// metric name and labels that won't be seen again.
	if (atomic.AddUint64(&h.opcount, 1) % 10000) == 0 {
		h.metrics.cleanup(time.Now().Add(-h.timeout()))
	}
}

func (h *Handler) timeout() time.Duration {
	if timeout := h.MetricTimeout; timeout != 0 {
		return timeout
	}
	return 2 * time.Minute
}

// ServeHTTP satsifies the http.Handler interface.
func (h *Handler) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	metrics := h.metrics.collect(make([]metric, 0, 10000))
	sort.Sort(byNameAndLabels(metrics))

	w := io.Writer(res)
	res.Header().Set("Content-Type", "text/plain; version=0.0.4")

	if acceptEncoding(req.Header.Get("Accept-Endoing"), "gzip") {
		res.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		w = zw
	}

	b := make([]byte, 1024)

	var lastMetricName string
	for i, m := range metrics {
		b = b[:0]
		name := m.rootName()

		if name == lastMetricName {
			// Silence the repeated output of type for values belonging to the
			// same metric.
			m.mtype, m.help = untyped, ""
		} else if i != 0 {
			// After every metric we want to output an empty line to make the
			// output easier to read.
			b = append(b, '\n')
		}

		w.Write(appendMetric(b, m))
		lastMetricName = name
	}
}

func acceptEncoding(accept string, check string) bool {
	for _, coding := range strings.Split(accept, ",") {
		if coding = strings.TrimSpace(coding); strings.HasPrefix(coding, check) {
			return true
		}
	}
	return false
}

type handleMetricCache struct {
	labels labels
}

var handleMetricPool = sync.Pool{
	New: func() interface{} {
		return &handleMetricCache{labels: make(labels, 0, 8)}
	},
}

func (cache *handleMetricCache) Len() int {
	return len(cache.labels)
}

func (cache *handleMetricCache) Swap(i int, j int) {
	cache.labels[i], cache.labels[j] = cache.labels[j], cache.labels[i]
}

func (cache *handleMetricCache) Less(i int, j int) bool {
	return cache.labels[i].less(cache.labels[j])
}

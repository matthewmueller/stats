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
	// Setting this field will trim this prefix from metric namespaces of the
	// metrics received by this handler.
	//
	// Unlike statsd-like systems, it is common for prometheus metrics to not
	// be prefixed and instead use labels to identify which service or group
	// of services the metrics are coming from. The intent of this field is to
	// provide support for this use case.
	//
	// Note that triming only applies to the metric namespace, the metric
	// name will always be left untouched.
	//
	// If empty, no prefix trimming is done.
	TrimPrefix string

	// MetricTimeout defines how long the handler exposes metrics that aren't
	// receiving updates.
	//
	// The default is to use a 2 minutes metric timeout.
	MetricTimeout time.Duration

	// Prometheus identifies unique time series by the combination of the metric
	// name and labels. Technically labels may be provided in any order, so they
	// need to be sorted to be properly matched against each other. However this
	// may be a expensive in high rate services, if a program can ensure it will
	// always present metrics with label names in the same order it may skip the
	// sorting step by setting this flag to true.
	//
	// Note that in the context of the stats package tags are usually always
	// presented in the same order since the APIs receive a slice of stats.Tag.
	// Unless the program is dynamically gneerating the slice of tags it's very
	// likely that it will be able to take advantage of skipping the sort.
	//
	// By default this flag is set to false to ensure correctness in every case.
	UseUnsortedLabels bool

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

	if !h.UseUnsortedLabels {
		sort.Sort(cache)
	}

	h.metrics.update(metric{
		mtype:  metricTypeOf(m.Type),
		scope:  strings.TrimPrefix(m.Namespace, h.TrimPrefix),
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
	switch req.Method {
	case "GET", "HEAD":
	default:
		res.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	metrics := h.metrics.collect(make([]metric, 0, 10000))
	sort.Sort(byNameAndLabels(metrics))

	w := io.Writer(res)
	res.Header().Set("Content-Type", "text/plain; version=0.0.4")

	if acceptEncoding(req.Header.Get("Accept-Encoding"), "gzip") {
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

// DefaultHandler is a prometheus handler configured to trim the default metric
// namespace off of metrics that it handles.
var DefaultHandler = &Handler{
	TrimPrefix: stats.DefaultEngine.Name(),
}

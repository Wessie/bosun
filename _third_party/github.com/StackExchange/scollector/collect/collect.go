// Package collect provides functions for sending data to OpenTSDB.
//
// The "collect" namespace is used (i.e., <metric_root>.collect) to collect
// program and queue metrics.
package collect

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/StackExchange/tsaf/_third_party/github.com/StackExchange/scollector/opentsdb"
	"github.com/StackExchange/tsaf/_third_party/github.com/mreiferson/go-httpclient"
)

var (
	// Freq is how often metrics are sent to OpenTSDB.
	Freq = time.Second * 15

	// MaxQueueLen is the maximum size of the queue, above which incoming data will
	// be discarded. Defaults to about 150MB.
	MaxQueueLen = 200000

	// BatchSize is the maximum length of data points sent at once to OpenTSDB.
	BatchSize = 50

	// Dropped is the number of dropped data points due to a full queue.
	dropped int64

	// Sent is the number of sent data points.
	sent int64

	tchan        chan *opentsdb.DataPoint
	tsdbURL      string
	osHostname   string
	metricRoot   string
	queue        opentsdb.MultiDataPoint
	qlock, mlock sync.Mutex
	counters                  = make(map[string]*addMetric)
	sets                      = make(map[string]*setMetric)
	client       *http.Client = &http.Client{
		Transport: &httpclient.Transport{
			RequestTimeout: time.Minute,
		},
	}
)

// InitChan is similar to Init, but uses the given channel instead of creating a
// new one.
func InitChan(tsdbhost, metric_root string, ch chan *opentsdb.DataPoint) error {
	if tchan != nil {
		return fmt.Errorf("cannot init twice")
	}
	if err := setHostName(); err != nil {
		return err
	}
	if err := checkClean(metric_root, "metric root"); err != nil {
		return err
	}
	if tsdbhost == "" {
		return fmt.Errorf("must specify non-empty tsdb host")
	}
	u := url.URL{
		Scheme: "http",
		Path:   "/api/put",
	}
	if !strings.Contains(tsdbhost, ":") {
		tsdbhost += ":4242"
	}
	u.Host = tsdbhost
	tsdbURL = u.String()
	metricRoot = metric_root + "."
	tchan = ch
	go func() {
		for dp := range tchan {
			if len(queue) > MaxQueueLen {
				dropped++
				continue
			}
			qlock.Lock()
			queue = append(queue, dp)
			qlock.Unlock()
		}
	}()
	go send()

	go collect()
	Set("collect.dropped", nil, func() int64 {
		return dropped
	})
	Set("collect.sent", nil, func() int64 {
		return sent
	})
	Set("collect.alloc", nil, func() int64 {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return int64(ms.Alloc)
	})
	Set("collect.goroutines", nil, func() int64 {
		return int64(runtime.NumGoroutine())
	})
	return nil
}

// Init sets up the channels and the queue for sending data to OpenTSDB. It also
// sets up the basename for all metrics.
func Init(tsdbhost, metric_root string) error {
	return InitChan(tsdbhost, metric_root, make(chan *opentsdb.DataPoint))
}

func setHostName() error {
	h, err := os.Hostname()
	if err != nil {
		return err
	}
	osHostname = strings.ToLower(strings.SplitN(h, ".", 2)[0])
	if err := checkClean(osHostname, "host tag"); err != nil {
		return err
	}
	return nil
}

type setMetric struct {
	metric string
	ts     opentsdb.TagSet
	f      func() int64
}

func Set(metric string, ts opentsdb.TagSet, f func() int64) error {
	if err := check(metric, &ts); err != nil {
		return err
	}
	tss := metric + ts.String()
	mlock.Lock()
	sets[tss] = &setMetric{metric, ts.Copy(), f}
	mlock.Unlock()
	return nil
}

type addMetric struct {
	metric string
	ts     opentsdb.TagSet
	value  int64
}

// Add takes a metric and increments a counter for that metric. The metric name
// is appended to the basename specified in the Init function.
func Add(metric string, inc int64, ts opentsdb.TagSet) error {
	if err := check(metric, &ts); err != nil {
		return err
	}
	tss := metric + ts.String()
	mlock.Lock()
	if counters[tss] == nil {
		counters[tss] = &addMetric{
			metric: metric,
			ts:     ts.Copy(),
		}
	}
	counters[tss].value += inc
	mlock.Unlock()
	return nil
}

func check(metric string, ts *opentsdb.TagSet) error {
	if tchan == nil {
		return fmt.Errorf("Init must be called before calling Add")
	}
	if err := checkClean(metric, "metric"); err != nil {
		return err
	}
	for k, v := range *ts {
		if err := checkClean(k, "tagk"); err != nil {
			return err
		}
		if err := checkClean(v, "tagv"); err != nil {
			return err
		}
	}
	if *ts == nil {
		*ts = make(opentsdb.TagSet)
	}
	if _, present := (*ts)["host"]; !present {
		(*ts)["host"] = osHostname
	}
	return nil
}

func checkClean(s, t string) error {
	if sc, err := opentsdb.Clean(s); s != sc || err != nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("%s %s may only contain a to z, A to Z, 0 to 9, -, _, ., / or Unicode letters and may not be empty", t, s)
	}
	return nil
}

func collect() {
	for {
		mlock.Lock()
		now := time.Now().Unix()
		for _, c := range counters {
			dp := &opentsdb.DataPoint{
				Metric:    metricRoot + c.metric,
				Timestamp: now,
				Value:     c.value,
				Tags:      c.ts,
			}
			tchan <- dp
		}
		for _, s := range sets {
			dp := &opentsdb.DataPoint{
				Metric:    metricRoot + s.metric,
				Timestamp: now,
				Value:     s.f(),
				Tags:      s.ts,
			}
			tchan <- dp
		}
		mlock.Unlock()
		time.Sleep(Freq)
	}
}

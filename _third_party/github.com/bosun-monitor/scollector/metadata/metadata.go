package metadata

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"sync"
	"time"

	"github.com/bosun-monitor/bosun/_third_party/github.com/bosun-monitor/scollector/opentsdb"
	"github.com/bosun-monitor/bosun/_third_party/github.com/bosun-monitor/scollector/util"
	"github.com/bosun-monitor/bosun/_third_party/github.com/StackExchange/slog"
)

type RateType string

const (
	Unknown RateType = ""
	Gauge            = "gauge"
	Counter          = "counter"
	Rate             = "rate"
)

type Unit string

const (
	None           Unit = ""
	A                   = "A" // Amps
	Bool                = "bool"
	Bytes               = "bytes"
	KBytes              = "kbytes"
	BytesPerSecond      = "bytes per second"
	C                   = "C" // Celsius
	Count               = ""
	Event               = ""
	Entropy             = "entropy"
	CHz                 = "CentiHertz"
	ContextSwitch       = "context switches"
	Interupt            = "interupts"
	Load                = "load"
	MHz                 = "MHz" // MegaHertz
	Ok                  = "ok"  // "OK" or not status, 0 = ok, 1 = not ok
	Page                = "pages"
	Pct                 = "percent" // Range of 0-100.
	PerSecond           = "per second"
	Process             = "processes"
	RPM                 = "RPM" // Rotations per minute.
	Second              = "seconds"
	Socket              = "sockets"
	StatusCode          = "status code"
	MilliSecond         = "milliseconds"
	V                   = "V" // Volts
	V_10                = "tenth-Volts"
	Megabit             = "Mbit"
	Operation           = "Operations"
)

type Metakey struct {
	Metric string
	Tags   string
	Name   string
}

func (m Metakey) TagSet() opentsdb.TagSet {
	tags, err := opentsdb.ParseTags(m.Tags)
	if err != nil {
		return nil
	}
	return tags
}

var (
	metadata  = make(map[Metakey]interface{})
	metalock  sync.Mutex
	metahost  string
	metafuncs []func()
	metadebug bool
)

func AddMeta(metric string, tags opentsdb.TagSet, name string, value interface{}, setHost bool) {
	if tags == nil {
		tags = make(opentsdb.TagSet)
	}
	if _, present := tags["host"]; setHost && !present {
		tags["host"] = util.Hostname
	}
	ts := tags.Tags()
	metalock.Lock()
	defer metalock.Unlock()
	prev, present := metadata[Metakey{metric, ts, name}]
	if !reflect.DeepEqual(prev, value) && present {
		slog.Infof("metadata changed for %s/%s/%s: %v to %v", metric, ts, name, prev, value)
	} else if metadebug {
		slog.Infof("AddMeta for %s/%s/%s: %v", metric, ts, name, value)
	}
	metadata[Metakey{metric, ts, name}] = value
}

func Init(u *url.URL, debug bool) error {
	mh, err := u.Parse("/api/metadata/put")
	if err != nil {
		return err
	}
	metahost = mh.String()
	metadebug = debug
	go collectMetadata()
	return nil
}

func collectMetadata() {
	// Wait a bit so hopefully our collectors have run once and populated the
	// metadata.
	time.Sleep(time.Second * 5)
	for {
		for _, f := range metafuncs {
			f()
		}
		sendMetadata()
		time.Sleep(time.Hour)
	}
}

type Metasend struct {
	Metric string          `json:",omitempty"`
	Tags   opentsdb.TagSet `json:",omitempty"`
	Name   string          `json:",omitempty"`
	Value  interface{}
	Time   time.Time `json:",omitempty"`
}

func sendMetadata() {
	metalock.Lock()
	if len(metadata) == 0 {
		metalock.Unlock()
		return
	}
	ms := make([]Metasend, len(metadata))
	i := 0
	for k, v := range metadata {
		ms[i] = Metasend{
			Metric: k.Metric,
			Tags:   k.TagSet(),
			Name:   k.Name,
			Value:  v,
		}
		i++
	}
	metalock.Unlock()
	b, err := json.MarshalIndent(&ms, "", "  ")
	if err != nil {
		slog.Error(err)
		return
	}
	resp, err := http.Post(metahost, "application/json", bytes.NewBuffer(b))
	if err != nil {
		slog.Error(err)
		return
	}
	if resp.StatusCode != 204 {
		slog.Error("bad metadata return:", resp.Status)
		return
	}
}

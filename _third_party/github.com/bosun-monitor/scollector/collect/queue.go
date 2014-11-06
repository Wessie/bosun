package collect

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/bosun-monitor/bosun/_third_party/github.com/StackExchange/slog"
	"github.com/bosun-monitor/bosun/_third_party/github.com/bosun-monitor/scollector/opentsdb"
)

func send() {
	for {
		qlock.Lock()
		if i := len(queue); i > 0 {
			if i > BatchSize {
				i = BatchSize
			}
			sending := queue[:i]
			queue = queue[i:]
			if Debug {
				slog.Infof("sending: %d, remaining: %d", i, len(queue))
			}
			qlock.Unlock()
			sendBatch(sending)
		} else {
			qlock.Unlock()
			time.Sleep(time.Second)
		}
	}
}

func sendBatch(batch opentsdb.MultiDataPoint) {
	if Print {
		for _, d := range batch {
			slog.Info(d.Telnet())
		}
		recordSent(len(batch))
		return
	}
	var buf bytes.Buffer
	g := gzip.NewWriter(&buf)
	if err := json.NewEncoder(g).Encode(batch); err != nil {
		slog.Error(err)
		return
	}
	if err := g.Close(); err != nil {
		slog.Error(err)
		return
	}
	req, err := http.NewRequest("POST", tsdbURL, &buf)
	if err != nil {
		slog.Error(err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
	}
	// Some problem with connecting to the server; retry later.
	if err != nil || resp.StatusCode != http.StatusNoContent {
		if err != nil {
			slog.Error(err)
		} else if resp.StatusCode != http.StatusNoContent {
			slog.Errorln(resp.Status)
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				slog.Error(err)
			}
			if len(body) > 0 {
				slog.Error(string(body))
			}
		}
		t := time.Now().Add(-time.Minute * 30).Unix()
		old := 0
		restored := 0
		for _, dp := range batch {
			if dp.Timestamp < t {
				old++
				continue
			}
			restored++
			tchan <- dp
		}
		if old > 0 {
			slog.Infof("removed %d old records", old)
		}
		d := time.Second * 5
		slog.Infof("restored %d, sleeping %s", restored, d)
		time.Sleep(d)
		return
	}
	recordSent(len(batch))
}

func recordSent(num int) {
	if Debug {
		slog.Infoln("sent", num)
	}
	slock.Lock()
	sent += int64(num)
	slock.Unlock()
}

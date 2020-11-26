package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/common/expfmt"
	log "github.com/sirupsen/logrus"

	"github.com/gogo/protobuf/proto"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
)

type Fetcher struct {
	metricStored *sync.Map
	v1api        v1.API
	labels       *model.LabelValues
	nbWorker     int
}

func NewFetcher(v1api v1.API, nbWorker int) (*Fetcher, error) {
	emptyLv := make(model.LabelValues, 0)
	f := &Fetcher{
		metricStored: &sync.Map{},
		v1api:        v1api,
		labels:       &emptyLv,
		nbWorker:     nbWorker,
	}
	err := f.retrieveLabels()
	return f, err
}

func (f *Fetcher) RenderExpFmt(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("error parsing form values: %v", err), http.StatusBadRequest)
		return
	}
	format := expfmt.Negotiate(req.Header)
	enc := expfmt.NewEncoder(w, format)
	metricFilter := make(map[string]bool)
	metricToFilter, metricFilterDefined := req.Form["metric[]"]
	if metricFilterDefined {
		for _, s := range metricToFilter {
			metricFilter[s] = true
		}
	}
	f.metricStored.Range(func(key, value interface{}) bool {
		if _, exists := metricFilter[key.(string)]; !exists && metricFilterDefined {
			return true
		}
		w.Write([]byte("\n"))
		if err := enc.Encode(value.(*dto.MetricFamily)); err != nil {
			log.Warningf("Error when encoding exp fmt: %s", err.Error())
		}
		return true
	})
}

func (f *Fetcher) RunRoutines() {
	go f.routineLabels()
	go f.routineFetching()
}

func (f *Fetcher) retrieveLabels() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	labels, _, err := f.v1api.LabelValues(ctx, model.MetricNameLabel, time.Time{}, time.Time{})
	if err != nil {
		return fmt.Errorf("Error when getting all labels: %s", err.Error())
	}
	*f.labels = labels
	return nil
}

func (f *Fetcher) routineLabels() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		err := f.retrieveLabels()
		if err != nil {
			log.WithField("routine", "labels").Warning(err.Error())
		}
		<-ticker.C
	}
}

func (f *Fetcher) fetchingWorker(metricNameChan <-chan string) {
	for metricName := range metricNameChan {
		f.registerMetric(metricName)
	}
}

func (f *Fetcher) routineFetching() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	labels := *f.labels
	for {
		jobs := make(chan string, len(labels))
		for w := 0; w < f.nbWorker; w++ {
			go f.fetchingWorker(jobs)
		}
		for _, labelValue := range labels {
			jobs <- string(labelValue)
		}
		close(jobs)
		ticker.Reset(1 * time.Minute)
		<-ticker.C
	}
}

func (f *Fetcher) registerMetric(metricName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	values, _, err := f.v1api.Query(ctx, metricName, time.Now())
	if err != nil {
		log.WithField("metric_name", metricName).Warningf("error when querying on metric store: %s", err.Error())
		return
	}
	vec := values.(model.Vector)
	var (
		lastMetricName string
		protMetricFam  *dto.MetricFamily
	)
	for _, s := range vec {
		nameSeen := false
		// globalUsed := map[string]struct{}{}
		protMetric := &dto.Metric{
			Untyped: &dto.Untyped{},
		}
		for name, value := range s.Metric {
			if value == "" {
				// No value means unset. Never consider those labels.
				// This is also important to protect against nameless metrics.
				continue
			}
			if name == model.MetricNameLabel {
				nameSeen = true
				if string(value) == lastMetricName {
					// We already have the name in the current MetricFamily,
					// and we ignore nameless metrics.
					continue
				}
				protMetricFam = &dto.MetricFamily{
					Type: dto.MetricType_UNTYPED.Enum(),
					Name: proto.String(string(value)),
				}
				lastMetricName = string(value)
				continue
			}
			protMetric.Label = append(protMetric.Label, &dto.LabelPair{
				Name:  proto.String(string(name)),
				Value: proto.String(string(value)),
			})
		}
		if !nameSeen {
			log.Debug("Ignoring nameless metric during federation: %#v", s.Metric)
			continue
		}
		protMetric.TimestampMs = proto.Int64(s.Timestamp.UnixNano() / int64(time.Millisecond))
		protMetric.Untyped.Value = proto.Float64(float64(s.Value))

		protMetricFam.Metric = append(protMetricFam.Metric, protMetric)
	}
	if protMetricFam == nil {
		return
	}
	f.metricStored.Store(metricName, protMetricFam)
}
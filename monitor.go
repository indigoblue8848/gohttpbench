package main

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Monitor struct {
	c         *Context
	collector chan *Record
	output    chan *Stats
}

type Stats struct {
	responseTimeData []time.Duration

	totalRequests       int
	totalExecutionTime  time.Duration
	totalResponseTime   time.Duration
	totalReceived       int64
	totalFailedReqeusts int

	errLength    int
	errConnect   int
	errReceive   int
	errException int
	errResponse  int
}

// for prom counters
var (
	hostName, _ = os.Hostname()
	totalRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gb_total_requests_count",
	}, []string{"host", "port", "url"})
	totalResponseTime = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gb_total_response_time",
	}, []string{"host", "port", "url"})
	totalReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gb_total_received_count",
	}, []string{"host", "port", "url"})
	totalFailedRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gb_total_failed_requests_count",
	}, []string{"host", "port", "url"})
)

// for prom counters
func init() {
	prometheus.MustRegister(totalRequests)
	prometheus.MustRegister(totalResponseTime)
	prometheus.MustRegister(totalReceived)
	prometheus.MustRegister(totalFailedRequests)

	f, err := os.OpenFile("./gb_log", os.O_RDWR | os.O_CREATE | os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	log.SetOutput(f)
}

func NewMonitor(context *Context, collector chan *Record) *Monitor {
	return &Monitor{context, collector, make(chan *Stats)}
}

func (m *Monitor) Run() {
	// for prom client
	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(fmt.Sprintf(":%d", m.c.config.pport), nil)

	// catch interrupt signal
	userInterrupt := make(chan os.Signal, 1)
	signal.Notify(userInterrupt, os.Interrupt)

	stats := &Stats{}
	stats.responseTimeData = make([]time.Duration, 0, m.c.config.requests)

	var timelimiter <-chan time.Time
	if m.c.config.timelimit > 0 {
		t := time.NewTimer(time.Duration(m.c.config.timelimit) * time.Second)
		defer t.Stop()
		timelimiter = t.C
	}

	// waiting for all of http workers to start
	m.c.start.Wait()

	fmt.Printf("Benchmarking %s (be patient)\n", m.c.config.host)
	sw := &StopWatch{}
	sw.Start()

loop:
	for {
		select {
		case record := <-m.collector:

			updateStats(stats, record, m.c.config.url, fmt.Sprintf("%d", m.c.config.pport))

			if record.Error != nil && !ContinueOnError {
				break loop
			}

			if stats.totalRequests >= 10 && stats.totalRequests%(m.c.config.requests/10) == 0 {
				fmt.Printf("Completed %d requests\n", stats.totalRequests)
			}

			if stats.totalRequests == m.c.config.requests {
				fmt.Printf("Finished %d requests\n", stats.totalRequests)
				break loop
			}

		case <-timelimiter:
			break loop
		case <-userInterrupt:
			break loop
		}
	}

	sw.Stop()
	stats.totalExecutionTime = sw.Elapsed

	// shutdown benchmark and all of httpworkers to stop
	close(m.c.stop)
	signal.Stop(userInterrupt)
	m.output <- stats
}

func updateStats(stats *Stats, record *Record, url string, port string) {
	stats.totalRequests++
	totalRequests.With(prometheus.Labels{"host":hostName, "port":port, "url":url}).Inc()

	if record.Error != nil {
		stats.totalFailedReqeusts++
		totalFailedRequests.With(prometheus.Labels{"host":hostName, "port":port, "url":url}).Inc()

		switch record.Error.(type) {
		case *ConnectError:
			stats.errConnect++
		case *ExceptionError:
			stats.errException++
		case *LengthError:
			stats.errLength++
		case *ReceiveError:
			stats.errReceive++
		case *ResponseError:
			stats.errResponse++
		default:
			stats.errException++
		}

		log.Printf("%s\n", record.Error.Error())

	} else {
		stats.totalResponseTime += record.responseTime
		totalResponseTime.With(prometheus.Labels{"host":hostName, "port":port, "url":url}).Add(record.responseTime.Seconds())
		stats.totalReceived += record.contentSize
		totalReceived.With(prometheus.Labels{"host":hostName, "port":port, "url":url}).Add(float64(record.contentSize))
		stats.responseTimeData = append(stats.responseTimeData, record.responseTime)
	}

}

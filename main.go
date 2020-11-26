package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	log "github.com/sirupsen/logrus"
)

func main() {
	var configFile string
	flag.StringVar(&configFile, "c", "config.yml", "Configuration File")
	flag.Parse()

	c, err := InitConfigFromFile(configFile)
	if err != nil {
		log.Fatal("Error loading config: ", err.Error())
	}

	certPool, err := c.LoadCertPool()
	if err != nil {
		log.Fatal("Error loading cert pool: ", err.Error())
	}

	certMetricStoreMtls, err := c.MetricStoreMTLS.ToCert()
	if err != nil {
		log.Fatal("Error loading cert for metric store mtls: ", err.Error())
	}

	transportMetricStore := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: c.SkipSSLValidation,
			RootCAs:            certPool,
			Certificates:       []tls.Certificate{certMetricStoreMtls},
		},
	}

	client, err := api.NewClient(api.Config{
		Address:      c.MetricStoreAPI,
		RoundTripper: transportMetricStore,
	})
	if err != nil {
		log.Fatal("Error when creating prometheus client: ", err.Error())
	}
	v1api := v1.NewAPI(client)
	fetcher, err := NewFetcher(v1api, c.NbWorkers)
	if err != nil {
		log.Fatal("Error when creating fetcher: ", err.Error())
	}
	fetcher.RunRoutines()

	http.HandleFunc("/metrics", fetcher.RenderExpFmt)

	listener, err := makeListener(c)
	if err != nil {
		log.Fatal(err.Error())
	}
	srv := &http.Server{Handler: http.DefaultServeMux}
	if err = srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %+s\n", err)
	}
}

func makeListener(c *Config) (net.Listener, error) {
	listenAddr := fmt.Sprintf("0.0.0.0:%d", c.Port)
	if !c.EnableSSL {
		log.Infof("Listen %s without tls ...", listenAddr)
		return net.Listen("tcp", listenAddr)
	}
	log.Infof("Listen %s with tls ...", listenAddr)
	certPool, err := c.LoadCertPool()
	if err != nil {
		return nil, err
	}

	cert, err := c.SSLCertificate.ToCert()
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    certPool,
	}

	tlsConfig.BuildNameToCertificate()
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(listener, tlsConfig), nil
}

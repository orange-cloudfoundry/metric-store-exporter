package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type TLSPem struct {
	CertChain  string `yaml:"cert_chain"`
	PrivateKey string `yaml:"private_key"`
}

func (p TLSPem) ToCert() (tls.Certificate, error) {
	certificate, err := tls.X509KeyPair([]byte(p.CertChain), []byte(p.PrivateKey))
	if err != nil {
		errMsg := fmt.Sprintf("Error loading key pair: %s", err.Error())
		return tls.Certificate{}, fmt.Errorf(errMsg)
	}
	return certificate, nil
}

type CAPool []string

func (cas CAPool) ToCertPool() (*x509.CertPool, error) {
	certPool, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}

	for _, ca := range cas {
		if ca == "" {
			continue
		}
		if ok := certPool.AppendCertsFromPEM([]byte(ca)); !ok {
			return nil, fmt.Errorf("Error while adding CACerts to gorouter's cert pool: \n%s\n", ca)
		}
	}
	return certPool, nil
}

type Log struct {
	Level   string `yaml:"level"`
	NoColor bool   `yaml:"no_color"`
	InJson  bool   `yaml:"in_json"`
}

func (c *Log) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type plain Log
	err := unmarshal((*plain)(c))
	if err != nil {
		return err
	}
	log.SetFormatter(&log.TextFormatter{
		DisableColors: c.NoColor,
	})
	if c.Level != "" {
		lvl, err := log.ParseLevel(c.Level)
		if err != nil {
			return err
		}
		log.SetLevel(lvl)
	}
	if c.InJson {
		log.SetFormatter(&log.JSONFormatter{})
	}

	return nil
}

type Config struct {
	SkipSSLValidation bool           `yaml:"skip_ssl_validation"`
	CAPool            CAPool         `yaml:"ca_pool"`
	Log               Log            `yaml:"log"`
	MetricStoreMTLS   TLSPem         `yaml:"metric_store_mtls"`
	SSLCertificate    TLSPem         `json:"ssl_certificate"`
	Port              uint16         `yaml:"port,omitempty"`
	EnableSSL         bool           `yaml:"enable_ssl,omitempty"`
	MetricStoreAPI    string         `yaml:"metric_store_api"`
	NbWorkers         int            `yaml:"nb_workers"`
	CertPool          *x509.CertPool `yaml:"-"`
}

func (c *Config) LoadCertPool() (*x509.CertPool, error) {
	if c.CertPool != nil {
		return c.CertPool, nil
	}
	var err error
	c.CertPool, err = c.CAPool.ToCertPool()
	if err != nil {
		return nil, err
	}
	return c.CertPool, nil
}

func InitConfigFromFile(path string) (*Config, error) {
	c := &Config{
		Port:           8086,
		MetricStoreAPI: "https://localhost:8080",
	}

	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	err = yaml.Unmarshal(b, c)
	if err != nil {
		return nil, err
	}
	if c.NbWorkers <= 0 {
		c.NbWorkers = 50
	}
	return c, nil
}

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"remaw/pkg/metrics"
	"time"

	log "github.com/sirupsen/logrus"
)

func textFormatter() log.Formatter {
	return &log.TextFormatter{
		DisableTimestamp: false,
		FullTimestamp:    true,
	}
}

func jsonFormatter() log.Formatter {
	return &log.JSONFormatter{
		TimestampFormat: time.RFC3339Nano,
	}
}

func run() error {
	var parameters Parameters

	flag.StringVar(&parameters.certFile, "cert", "./cert.pem", "File containing the x509 Certificate for HTTPS.")
	flag.StringVar(&parameters.keyFile, "key", "./key.pem", "File containing the x509 private key to --tlsCertFile.")
	flag.StringVar(&parameters.LogFormat, "log-format", "text", "Log format, either 'json' or 'text'")
	flag.StringVar(&parameters.LogLevel, "log-level", "info", "Logging verbosity level")
	flag.Parse()

	switch parameters.LogFormat {
	case "json":
		log.SetFormatter(jsonFormatter())
	case "text":
		log.SetFormatter(textFormatter())
	default:
		return fmt.Errorf("log format '%s' is not recognized", parameters.LogFormat)
	}

	logLevel, err := log.ParseLevel(parameters.LogLevel)
	if err != nil {
		return fmt.Errorf("while setting log level: %s", err)
	}
	log.SetLevel(logLevel)

	pair, err := tls.LoadX509KeyPair(parameters.certFile, parameters.keyFile)
	if err != nil {
		return fmt.Errorf("failed to load key pair: %v", err)
	}

	go metrics.Serve(":8080", "/metrics", "/isReady", "/isAlive")

	webhookServer := WebhookServer{
		server: &http.Server{
			Addr:      ":8443",
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}},
		},
	}

	http.HandleFunc("/mutate", webhookServer.serve)

	err = webhookServer.server.ListenAndServeTLS("", "")
	if err != nil {
		return fmt.Errorf("while starting server: %s", err)
	}

	log.Info("Shutting down cleanly")
	return nil
}

func main() {
	err := run()
	if err != nil {
		log.Errorf("Fatal error: %s", err)
		os.Exit(1)
	}
}

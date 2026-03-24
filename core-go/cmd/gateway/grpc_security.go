package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/ai-gateway/core/internal/config"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func buildGRPCTransportCredentials(cfg *config.Config) (credentials.TransportCredentials, error) {
	if !cfg.GRPCEnableTLS {
		return insecure.NewCredentials(), nil
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if cfg.GRPCServerName != "" {
		tlsConfig.ServerName = cfg.GRPCServerName
	}

	if cfg.GRPCCAFile != "" {
		caPEM, err := os.ReadFile(cfg.GRPCCAFile)
		if err != nil {
			return nil, fmt.Errorf("read GRPC_CA_FILE: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("parse GRPC_CA_FILE: no certificates found")
		}
		tlsConfig.RootCAs = pool
	}

	if cfg.GRPCClientCertFile != "" || cfg.GRPCClientKeyFile != "" {
		if cfg.GRPCClientCertFile == "" || cfg.GRPCClientKeyFile == "" {
			return nil, fmt.Errorf("both GRPC_CLIENT_CERT_FILE and GRPC_CLIENT_KEY_FILE are required for mTLS")
		}
		cert, err := tls.LoadX509KeyPair(cfg.GRPCClientCertFile, cfg.GRPCClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load gRPC client keypair: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return credentials.NewTLS(tlsConfig), nil
}

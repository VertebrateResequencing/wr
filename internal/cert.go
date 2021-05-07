// Copyright © 2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
// This file was based on https://ericchiang.github.io/post/go-tls/ Copyright
// © 2015 Eric Chiang.
//
//  This file is part of wr.
//
//  wr is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  wr is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with wr. If not, see <http://www.gnu.org/licenses/>.

package internal

// this file has functions for generating certs and keys for TLS purposes

import (
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

const validFor = 365 * 24 * time.Hour

// Err* constants are found in our returned CertError under err.Type, so you can
// cast and check if it's a certain type of error.
const (
	ErrParseCert   = "could not be parsed"
	ErrExpiredCert = "is expired"
)

// CertError records a certificate-related error.
type CertError struct {
	Type string // ErrParseCert or ErrExpiredCert
	Path string // path to the certificate file
	Err  error  // In the case of ErrParseCert, the parsing error
}

func (e CertError) Error() string {
	msg := e.Path + " " + e.Type
	if e.Err != nil {
		msg += " [" + e.Err.Error() + "]"
	}

	return msg
}

// CheckCerts checks if the given cert and key file are readable. If one or
// both of them are not, returns an error.
func CheckCerts(serverPemFile string, serverKeyFile string) error {
	if _, err := os.Stat(serverPemFile); err != nil {
		return err
	} else if _, err := os.Stat(serverKeyFile); err != nil {
		return err
	}
	return nil
}

// CertExpiry returns the time that the certificate given by the path to a pem
// file will expire.
func CertExpiry(certFile string) (time.Time, error) {
	certPEMBlock, err := os.ReadFile(certFile)
	if err != nil {
		return time.Now(), err
	}

	var cert tls.Certificate
	for {
		var certDERBlock *pem.Block
		certDERBlock, certPEMBlock = pem.Decode(certPEMBlock)
		if certDERBlock == nil {
			break
		}
		if certDERBlock.Type == "CERTIFICATE" {
			cert.Certificate = append(cert.Certificate, certDERBlock.Bytes)
		}
	}

	if len(cert.Certificate) == 0 {
		return time.Now(), CertError{Type: ErrParseCert, Path: certFile}
	}

	// We don't need to parse the public key for TLS, but we so do anyway
	// to check that it looks sane and matches the private key.
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return time.Now(), CertError{Type: ErrParseCert, Path: certFile, Err: err}
	}

	return x509Cert.NotAfter, nil
}

// GenerateCerts creates a CA certificate which is used to sign a created server
// certificate which will have a corresponding key, all saved as PEM files. An
// error is generated if any of the files already exist.
func GenerateCerts(caFile, serverPemFile, serverKeyFile, domain string) error {
	_, err := os.Stat(caFile)
	if err == nil {
		return fmt.Errorf("ca cert [%s] already exists", caFile)
	}
	_, err = os.Stat(serverPemFile)
	if err == nil {
		return fmt.Errorf("server cert [%s] already exists", serverPemFile)
	}
	_, err = os.Stat(serverKeyFile)
	if err == nil {
		return fmt.Errorf("server key [%s] already exists", serverKeyFile)
	}

	// key for root CA
	rootKey, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		return err
	}

	// cert for root CA, self-signed
	rootCertTmpl, err := certTemplate(domain)
	if err != nil {
		return err
	}
	rootCertTmpl.IsCA = true
	rootCertTmpl.KeyUsage |= x509.KeyUsageCertSign
	rootCert, err := createCert(rootCertTmpl, rootCertTmpl, &rootKey.PublicKey, rootKey, caFile)
	if err != nil {
		return err
	}

	// key for server
	serverKey, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		return err
	}

	// cert for server, signed by root CA
	servCertTmpl, err := certTemplate(domain)
	if err != nil {
		return err
	}
	_, err = createCert(servCertTmpl, rootCert, &serverKey.PublicKey, rootKey, serverPemFile)
	if err != nil {
		return err
	}

	// store the server's key
	keyOut, err := os.OpenFile(serverKeyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	err = pem.Encode(keyOut, &pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey),
	})
	if err != nil {
		return err
	}
	return keyOut.Close()
}

// certTemplate creates a certificate template with a random serial number,
// valid from now until validFor. It will be valid for supplied domain.
func certTemplate(domain string) (*x509.Certificate, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := crand.Int(crand.Reader, serialNumberLimit)
	if err != nil {
		return nil, errors.New("failed to generate serial number: " + err.Error())
	}

	template := x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               pkix.Name{Organization: []string{"wr manager"}},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(validFor),
		IPAddresses:           []net.IP{net.ParseIP("0.0.0.0"), net.ParseIP("127.0.0.1")},
		DNSNames:              []string{domain},
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	return &template, nil
}

// createCert creates a certificate given a template, signing it against its
// parent, and saving the cert in PEM format to certPath.
func createCert(template, parentCert *x509.Certificate, publicKey interface{}, parentPrivateKey interface{}, certPath string) (*x509.Certificate, error) {
	certDER, err := x509.CreateCertificate(crand.Reader, template, parentCert, publicKey, parentPrivateKey)
	if err != nil {
		return nil, err
	}

	// parse the resulting certificate so we can use it again
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}

	// save in PEM format
	b := pem.Block{Type: "CERTIFICATE", Bytes: certDER}
	certOut, err := os.Create(certPath)
	if err != nil {
		err = fmt.Errorf("creation of certificate file [%s] failed: %s", certPath, err)
		return nil, err
	}
	err = pem.Encode(certOut, &b)
	if err != nil {
		return nil, err
	}
	err = certOut.Close()
	return cert, err
}

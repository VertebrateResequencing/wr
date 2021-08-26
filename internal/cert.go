// Copyright © 2018-2021 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>, Ashwini Chhipa <ac55@sanger.ac.uk>
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

import (
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"net"
	"os"
	"time"
)

const (
	// validFor is time duration of certificate validity.
	validFor = 365 * 24 * time.Hour

	// certFileFlags are flags for certificate files.
	certFileFlags int = os.O_RDWR | os.O_CREATE | os.O_TRUNC

	// certMode is the file mode for certificate file.
	certMode os.FileMode = 0666

	// serverKeyFlags are flags for server key file.
	serverKeyFlags int = os.O_WRONLY | os.O_CREATE | os.O_TRUNC

	// serverKeyMode is the file mode for server key file.
	serverKeyMode os.FileMode = 0600

	// bits for rsa keys.
	DefaultBitsForRootRSAKey   int = 2048
	DefualtBitsForServerRSAKey int = 2048

	// certFileFlags are the certificate file flags.
	DefaultCertFileFlags int = os.O_RDWR | os.O_CREATE | os.O_TRUNC
)

// CertificateErr is supplied to CertError to define the certain type of
// certificate related errors.
type CertificateErr string

// ErrCert* are the reasons related to certificates.
const (
	ErrCertParse    CertificateErr = "could not be parsed"
	ErrCertCreate   CertificateErr = "could not be created"
	ErrCertExists   CertificateErr = "already exists"
	ErrCertEncode   CertificateErr = "could not encode"
	ErrCertNotFound CertificateErr = "cert could not be found"
	ErrExpiredCert  CertificateErr = "cert expired"
)

// CertError records a certificate-related error.
type CertError struct {
	Type CertificateErr // one of our CertificateErr constants
	Path string         // path to the certificate file
	Err  error          // In the case of ErrParseCert, the parsing error
}

// Error returns an error with a reason related to certificate and its path.
func (ce CertError) Error() string {
	msg := ce.Path + " " + string(ce.Type)
	if ce.Err != nil {
		msg += " [" + ce.Err.Error() + "]"
	}

	return msg
}

// NumberError records a number related error.
type NumberError struct {
	Err error
}

// Error returns a number related error.
func (n *NumberError) Error() string {
	return fmt.Sprintf("failed to generate serial number: %s", n.Err)
}

// GenerateCerts creates a CA certificate which is used to sign a created server
// certificate which will have a corresponding key, all saved as PEM files. An
// error is generated if any of the files already exist.
//
// randReader := crand.Reader to be declared in calling function.
func GenerateCerts(caFile, serverPemFile, serverKeyFile, domain string,
	bitsForRootRSAKey int, bitsForServerRSAKey int, randReader io.Reader, fileFlags int) error {
	err := checkIfCertsExist([]string{caFile, serverPemFile, serverKeyFile})
	if err != nil {
		return err
	}

	rootKey, err := rsa.GenerateKey(crand.Reader, bitsForRootRSAKey)
	if err != nil {
		return err
	}

	serverKey, err := rsa.GenerateKey(crand.Reader, bitsForServerRSAKey)
	if err != nil {
		return err
	}

	err = generateCertificates(caFile, domain, rootKey, serverKey, serverPemFile, randReader, fileFlags)
	if err != nil {
		return err
	}

	pemBlock := &pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}
	err = encodeAndSavePEM(pemBlock, serverKeyFile, serverKeyFlags, serverKeyMode)

	return err
}

// checkIfCertsExist checks if any of the certificate files exist, if yes then
// returns error.
func checkIfCertsExist(certFiles []string) error {
	for _, cFile := range certFiles {
		if _, err := os.Stat(cFile); err == nil {
			return &CertError{Type: ErrCertExists, Path: cFile, Err: err}
		}
	}

	return nil
}

// generateCertificates generates root and server certificates.
func generateCertificates(caFile, domain string, rootKey *rsa.PrivateKey, serverKey *rsa.PrivateKey,
	serverPemFile string, randReader io.Reader, fileFlags int) error {
	rootServerTemplates := make([]*x509.Certificate, 2)
	for i := 0; i < len(rootServerTemplates); i++ {
		certTmplt, err := certTemplate(domain, randReader)
		if err != nil {
			return err
		}

		rootServerTemplates[i] = certTmplt
	}

	rootServerTemplates[0].IsCA = true
	rootServerTemplates[0].KeyUsage |= x509.KeyUsageCertSign

	rootCert, err := generateRootCert(caFile, rootServerTemplates[0], rootKey, randReader, fileFlags)
	if err != nil {
		return err
	}

	err = generateServerCert(serverPemFile, rootCert, rootServerTemplates[1], rootKey, serverKey,
		randReader, fileFlags)

	return err
}

// certTemplate creates a certificate template with a random serial number,
// valid from now until validFor. It will be valid for supplied domain.
func certTemplate(domain string, randReader io.Reader) (*x509.Certificate, error) {
	serialNumLimit := new(big.Int).Lsh(big.NewInt(1), 128)

	serialNumber, err := crand.Int(randReader, serialNumLimit)
	if err != nil {
		return nil, &NumberError{Err: err}
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

// generateRootCert generates and returns root certificate.
func generateRootCert(caFile string, template *x509.Certificate, rootKey *rsa.PrivateKey,
	randReader io.Reader, fileFlags int) (*x509.Certificate, error) {
	rootCertByte, err := createCertFromTemplate(template, template, &rootKey.PublicKey, rootKey, randReader)
	if err != nil {
		return nil, err
	}

	rootCert, err := parseCertAndSavePEM(rootCertByte, caFile, fileFlags)
	if err != nil {
		return nil, err
	}

	return rootCert, err
}

// createCertFromTemplate creates a certificate given a template, siginign it
// against its parent. Returned in DER encoding.
func createCertFromTemplate(template, parentCert *x509.Certificate, pubKey interface{},
	parentPvtKey interface{}, randReader io.Reader) ([]byte, error) {
	certDER, err := x509.CreateCertificate(randReader, template, parentCert, pubKey, parentPvtKey)
	if err != nil {
		return nil, &CertError{Type: ErrCertCreate, Err: err}
	}

	return certDER, nil
}

// parseCertAndSavePEM parses the certificate to reuse it and saves it in PEM
// format to certPath.
func parseCertAndSavePEM(certByte []byte, certPath string, flags int) (*x509.Certificate, error) {
	cert, err := x509.ParseCertificate(certByte)
	if err != nil {
		return nil, &CertError{Type: ErrCertParse, Path: certPath, Err: err}
	}

	block := &pem.Block{Type: "CERTIFICATE", Bytes: certByte}

	err = encodeAndSavePEM(block, certPath, flags, certMode)
	if err != nil {
		return nil, err
	}

	return cert, nil
}

// encodeAndSavePEM encodes the certificate and saves it in PEM format.
func encodeAndSavePEM(block *pem.Block, certPath string, flags int, mode os.FileMode) error {
	certOut, err := os.OpenFile(certPath, flags, mode)
	if err != nil {
		return &CertError{Type: ErrCertCreate, Path: certPath, Err: err}
	}

	err = pem.Encode(certOut, block)
	if err != nil {
		return &CertError{Type: ErrCertEncode, Path: certPath, Err: err}
	}

	err = certOut.Close()

	return err
}

// generateServerCert generates and returns server certificate signed by root
// CA.
func generateServerCert(serverPemFile string, rootCert *x509.Certificate, template *x509.Certificate,
	rootKey *rsa.PrivateKey, serverKey *rsa.PrivateKey, randReader io.Reader, fileFlags int) error {
	servCertBtye, err := createCertFromTemplate(template, rootCert, &serverKey.PublicKey, rootKey, randReader)
	if err != nil {
		return err
	}

	_, err = parseCertAndSavePEM(servCertBtye, serverPemFile, fileFlags)
	if err != nil {
		return err
	}

	return err
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
	certPEMBlock, err := ioutil.ReadFile(certFile)
	if err != nil {
		return time.Now(), err
	}

	cert := findPEMBlockAndReturnCert(certPEMBlock)
	if len(cert.Certificate) == 0 {
		return time.Now(), &CertError{Type: ErrCertNotFound, Path: certFile}
	}

	// We don't need to parse the public key for TLS, but we so do anyway
	// to check that it looks sane and matches the private key.
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return time.Now(), &CertError{Type: ErrCertParse, Path: certFile, Err: err}
	}

	return x509Cert.NotAfter, nil
}

// findPEMBlockAndReturnCert finds the next PEM formatted block in the input
// and then returns a tls certificate.
func findPEMBlockAndReturnCert(certPEMBlock []byte) tls.Certificate {
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

	return cert
}

// Copyright © 2018-2021 Genome Research Limited
// Author: Ashwini Chhipa <ac55@sanger.ac.uk>
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
	"bytes"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

const (
	blockFileWrite int         = os.O_RDONLY | os.O_CREATE | os.O_TRUNC
	fileMode       os.FileMode = 0o600
)

func TestCert(t *testing.T) {
	Convey("Given the certificates key file paths", t, func() {
		certtmpdir, err1 := ioutil.TempDir(".", "wr_jobqueue_cert_dir_")
		if err1 != nil {
			log.Fatal(err1)
		}
		defer os.RemoveAll(certtmpdir)

		caFile := filepath.Join(certtmpdir, "ca.pem")
		certFile := filepath.Join(certtmpdir, "cert.pem")
		keyFile := filepath.Join(certtmpdir, "key.pem")
		certDomain := "localhost"

		Convey("it can check that they don't exist", func() {
			err := checkIfCertsExist([]string{caFile, certFile, keyFile})
			So(err, ShouldBeNil)
		})

		Convey("Given an RSA key and a certificate template", func() {
			rsaKey, err := rsa.GenerateKey(crand.Reader, DefaultBitsForRootRSAKey)
			So(err, ShouldBeNil)
			So(rsaKey, ShouldNotBeNil)

			r := bytes.NewReader([]byte{})
			errCertTmplt, err := certTemplate(certDomain, r)
			So(err, ShouldNotBeNil)
			So(errCertTmplt, ShouldBeNil)

			certTmplt, err := certTemplate(certDomain, crand.Reader)
			So(err, ShouldBeNil)
			So(certTmplt, ShouldNotBeNil)

			Convey("it can create a certificate from it", func() {
				Convey("not when an empty template is used", func() {
					testTmpl := x509.Certificate{}
					certByte, err := createCertFromTemplate(&testTmpl, certTmplt, &rsaKey.PublicKey, rsaKey, crand.Reader)
					So(err, ShouldNotBeNil)
					So(certByte, ShouldBeNil)
				})

				Convey("when a non-empty template is used", func() {
					certByte, err := createCertFromTemplate(certTmplt, certTmplt, &rsaKey.PublicKey, rsaKey, crand.Reader)
					So(err, ShouldBeNil)
					So(certByte, ShouldNotBeNil)

					Convey("and given a pemblock, it can encode and save pem file", func() {
						pemBlock := &pem.Block{Type: "CERTIFICATE", Bytes: certByte}
						Convey("when file can be written", func() {
							err = encodeAndSavePEM(pemBlock, caFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fileMode)
							So(err, ShouldBeNil)
						})

						Convey("not when file cannot be created", func() {
							err = encodeAndSavePEM(pemBlock, caFile, os.O_RDONLY, fileMode)
							So(err, ShouldNotBeNil)
						})

						Convey("not when file cannot be written", func() {
							err = encodeAndSavePEM(pemBlock, caFile, blockFileWrite, fileMode)
							So(err, ShouldNotBeNil)
						})
					})

					Convey("and parse the Certificate", func() {
						Convey("for a non-empty certificate template byte", func() {
							cert, err := parseCertAndSavePEM(certByte, caFile, certFileFlags)
							So(cert, ShouldNotBeNil)
							So(err, ShouldBeNil)
						})

						Convey("but not for a empty certificate template byte", func() {
							empByte := []byte{}
							errCert, err := parseCertAndSavePEM(empByte, caFile, certFileFlags)
							So(errCert, ShouldBeNil)
							So(err, ShouldNotBeNil)
						})

						Convey("and not when file cannot be written", func() {
							cert, err := parseCertAndSavePEM(certByte, caFile, blockFileWrite)
							So(cert, ShouldBeNil)
							So(err, ShouldNotBeNil)
						})
					})
				})

				Convey("generate a root certificate", func() {
					rootCert, err := generateRootCert(caFile, certTmplt, rsaKey, crand.Reader, certFileFlags)
					So(rootCert, ShouldNotBeNil)
					So(err, ShouldBeNil)

					Convey("not with an empty template", func() {
						empRootCert, err := generateRootCert(caFile, &x509.Certificate{}, rsaKey, crand.Reader, certFileFlags)
						So(empRootCert, ShouldBeNil)
						So(err, ShouldNotBeNil)
					})

					Convey("and not when file cannot be written", func() {
						empRootCert, err := generateRootCert(caFile, certTmplt, rsaKey, crand.Reader, blockFileWrite)
						So(empRootCert, ShouldBeNil)
						So(err, ShouldNotBeNil)
					})

					Convey("and then generate a server certificate", func() {
						err := generateServerCert(certFile, rootCert, certTmplt, rsaKey, rsaKey, crand.Reader, certFileFlags)
						So(err, ShouldBeNil)

						Convey("not with an empty template", func() {
							err = generateServerCert(certFile, rootCert, &x509.Certificate{}, rsaKey, rsaKey, crand.Reader, certFileFlags)
							So(err, ShouldNotBeNil)
						})

						Convey("and not when file cannot be written", func() {
							err = generateServerCert(certFile, rootCert, certTmplt, rsaKey, rsaKey, crand.Reader, blockFileWrite)
							So(err, ShouldNotBeNil)
						})
					})
				})
			})
		})

		Convey("and an RSA key, it can generate both root and server certificates", func() {
			rsaKey, err := rsa.GenerateKey(crand.Reader, DefaultBitsForRootRSAKey)
			So(err, ShouldBeNil)

			err = generateCertificates(caFile, certDomain, rsaKey, rsaKey, certFile, crand.Reader, certFileFlags)
			So(err, ShouldBeNil)

			Convey("not with an empty serial number in template", func() {
				err = generateCertificates(caFile, certDomain, rsaKey, rsaKey, certFile, bytes.NewReader([]byte{}), certFileFlags)
				So(err, ShouldNotBeNil)
			})

			Convey("and not when files cannot be written", func() {
				err = generateCertificates(caFile, certDomain, rsaKey, rsaKey, certFile, crand.Reader, blockFileWrite)
				So(err, ShouldNotBeNil)
			})

			Convey("and it can store the server's private key", func() {
				pemBlock := &pem.Block{
					Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
				}
				err = encodeAndSavePEM(pemBlock, keyFile, serverKeyFlags, serverKeyMode)
				So(err, ShouldBeNil)
			})
		})

		Convey("it can generate the root and server certificate", func() {
			Convey("not when zero bits for root rsa key is used", func() {
				err := GenerateCerts(caFile, certFile, keyFile, certDomain, 0, DefaultBitsForRootRSAKey, crand.Reader, certFileFlags)
				So(err, ShouldNotBeNil)
			})

			Convey("not when zero bits for server rsa key is used", func() {
				err := GenerateCerts(caFile, certFile, keyFile, certDomain, DefaultBitsForRootRSAKey, 0, crand.Reader, certFileFlags)
				So(err, ShouldNotBeNil)
			})

			Convey("not when files cannot be written", func() {
				err := GenerateCerts(caFile, certFile, keyFile, certDomain, DefaultBitsForRootRSAKey, DefaultBitsForRootRSAKey, crand.Reader,
					blockFileWrite)
				So(err, ShouldNotBeNil)
			})

			Convey("when bits and file flags are correct", func() {
				err := GenerateCerts(caFile, certFile, keyFile, certDomain, DefaultBitsForRootRSAKey, DefaultBitsForRootRSAKey, crand.Reader,
					certFileFlags)
				So(err, ShouldBeNil)

				Convey("check if cert files exists", func() {
					err = checkIfCertsExist([]string{caFile, certFile, keyFile})
					So(err, ShouldNotBeNil)
				})

				Convey("trying to generate certificates again will fail", func() {
					err = GenerateCerts(caFile, certFile, keyFile, certDomain, DefaultBitsForRootRSAKey, DefaultBitsForRootRSAKey, crand.Reader,
						certFileFlags)
					So(err, ShouldNotBeNil)
				})

				Convey("Check if certificate files are readable", func() {
					err = CheckCerts(certFile, keyFile)
					So(err, ShouldBeNil)
					err = CheckCerts("/tmp/random.pem", keyFile)
					So(err, ShouldNotBeNil)
					err = CheckCerts(certFile, "/tmp/random.pem")
					So(err, ShouldNotBeNil)
				})

				Convey("Find PEM Block in a file and Return Certifcate", func() {
					certPEMBlock, err := ioutil.ReadFile(certFile)
					So(err, ShouldBeNil)

					ccert := findPEMBlockAndReturnCert(certPEMBlock)
					So(ccert, ShouldNotBeNil)

					ccert1 := findPEMBlockAndReturnCert([]byte{})
					So(len(ccert1.Certificate), ShouldEqual, 0)
				})

				Convey("Check that certificate expires in a year", func() {
					expiry, err := CertExpiry(caFile)
					So(err, ShouldBeNil)
					So(expiry, ShouldHappenBetween, time.Now().Add(364*24*time.Hour), time.Now().Add(366*24*time.Hour))

					_, err = CertExpiry("/tmp/exp.pem")
					So(err, ShouldNotBeNil)

					empCertFile := filepath.Join(certtmpdir, "emp.pem")
					err = ioutil.WriteFile(empCertFile, []byte{0}, fileMode)
					So(err, ShouldBeNil)

					expiry, err = CertExpiry(empCertFile)
					So(expiry, ShouldNotBeNil)
					So(err, ShouldNotBeNil)
				})
			})
		})
	})
}

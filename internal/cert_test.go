/*******************************************************************************
 * Copyright (c) 2021, 2024-2025 Genome Research Ltd.
 *
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package internal

import (
	"bytes"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
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
		certtmpdir := t.TempDir()
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
			So(wrappedErrIs(err), ShouldBeTrue)

			certTmplt, err := certTemplate(certDomain, crand.Reader)
			So(err, ShouldBeNil)
			So(certTmplt, ShouldNotBeNil)

			Convey("it can create a certificate from it", func() {
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
							So(wrappedErrIs(err), ShouldBeTrue)
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

					Convey("not when file cannot be written", func() {
						empRootCert, err := generateRootCert(caFile, certTmplt, rsaKey, crand.Reader, blockFileWrite)
						So(empRootCert, ShouldBeNil)
						So(err, ShouldNotBeNil)
					})

					Convey("and then generate a server certificate", func() {
						err := generateServerCert(certFile, rootCert, certTmplt, rsaKey, rsaKey, crand.Reader, certFileFlags)
						So(err, ShouldBeNil)

						Convey("not when file cannot be written", func() {
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
					certPEMBlock, err := os.ReadFile(certFile)
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
					err = os.WriteFile(empCertFile, []byte{0}, fileMode)
					So(err, ShouldBeNil)

					expiry, err = CertExpiry(empCertFile)
					So(expiry, ShouldNotBeNil)
					So(err, ShouldNotBeNil)
				})
			})
		})
	})
}

func wrappedErrIs(err error) bool {
	var certErr *CertError
	if errors.As(err, &certErr) && certErr.Err != nil {
		return errors.Is(err, certErr.Err)
	}

	var numberErr *NumberError
	if errors.As(err, &numberErr) && numberErr.Err != nil {
		return errors.Is(err, numberErr.Err)
	}

	return false
}

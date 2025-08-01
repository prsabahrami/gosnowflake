package gosnowflake

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

const testCrlServerPort = 56894

var serialNumber = int64(0) // to be incremented

type cacheValidityTimeType time.Duration

type allowCertificatesWithoutCrlURLType bool

type inMemoryCacheDisabledType bool
type onDiskCacheDisabledType bool

type notAfterType time.Time

type revokedCert *x509.Certificate

type thisUpdateType time.Time
type nextUpdateType time.Time

func newTestCrlValidator(t *testing.T, checkMode CertRevocationCheckMode, args ...any) *crlValidator {
	httpClient := &http.Client{}
	cacheValidityTime := 5 * time.Minute
	allowCertificatesWithoutCrlURL := false
	inMemoryCacheDisabled := false
	onDiskCacheDisabled := false
	for _, arg := range args {
		switch v := arg.(type) {
		case *http.Client:
			httpClient = v
		case cacheValidityTimeType:
			cacheValidityTime = time.Duration(v)
		case allowCertificatesWithoutCrlURLType:
			allowCertificatesWithoutCrlURL = bool(v)
		case inMemoryCacheDisabledType:
			inMemoryCacheDisabled = bool(v)
		case onDiskCacheDisabledType:
			onDiskCacheDisabled = bool(v)
		default:
			t.Fatalf("unexpected argument type %T", v)
		}
	}
	cacheDir := t.TempDir()
	return newCrlValidator(checkMode, allowCertificatesWithoutCrlURL, cacheValidityTime, inMemoryCacheDisabled, onDiskCacheDisabled, cacheDir, httpClient)
}

func TestCrlCheckModeDisabled_NoHttpCall(t *testing.T) {
	caKey, caCert := createCa(t, nil, nil, "root CA", "/rootCrl")
	_, leafCert := createLeafCert(t, caCert, caKey, "/rootCrl")
	crt := &countingRoundTripper{}
	cv := newTestCrlValidator(t, CertRevocationCheckDisabled, &http.Client{Transport: crt})
	err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
	assertNilE(t, err)
	assertEqualE(t, crt.totalRequests(), 0, "no HTTP request should be made when check mode is disabled")
}

func TestCrlModes(t *testing.T) {
	for _, checkMode := range []CertRevocationCheckMode{CertRevocationCheckEnabled, CertRevocationCheckAdvisory} {
		t.Run(fmt.Sprintf("checkMode=%v", checkMode), func(t *testing.T) {
			t.Run("ShortLivedCertDoesNotNeedCRL", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "", notAfterType(time.Now().Add(4*24*time.Hour)))

				cv := newTestCrlValidator(t, checkMode, allowCertificatesWithoutCrlURLType(false))
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				assertNilE(t, err)
			})

			t.Run("LeafCertNotRevoked", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
				crl := createCrl(t, caCert, caPrivateKey)

				server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				assertNilE(t, err)
			})

			t.Run("LeafCertRevoked", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
				crl := createCrl(t, caCert, caPrivateKey, revokedCert(leafCert))

				server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				assertNotNilF(t, err)
				assertEqualE(t, err.Error(), "every verified certificate chain contained revoked certificates")
			})

			t.Run("TestLeafNotRevokedAndRootDoesNotProvideCrl", func(t *testing.T) {
				rootCaPrivateKey, rootCaCert := createCa(t, nil, nil, "root CA", "")
				intermediateCaKey, intermediateCaCert := createCa(t, rootCaCert, rootCaPrivateKey, "intermediate CA", "")
				_, leafCert := createLeafCert(t, intermediateCaCert, intermediateCaKey, "/intermediateCrl")
				intermediateCrl := createCrl(t, intermediateCaCert, intermediateCaKey)

				server := createCrlServer(t, newCrlEndpointDef("/intermediateCrl", intermediateCrl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, intermediateCaCert, rootCaCert}})
				if checkMode == CertRevocationCheckEnabled {
					assertEqualE(t, err.Error(), "certificate revocation check failed")
				} else {
					assertNilE(t, err)
				}
			})

			t.Run("IntermediateRevokedAndIntermediateDoesNotProvideCrl", func(t *testing.T) {
				rootCaPrivateKey, rootCaCert := createCa(t, nil, nil, "root CA", "")
				intermediateCaKey, intermediateCaCert := createCa(t, rootCaCert, rootCaPrivateKey, "intermediate CA", "/rootCrl")
				_, leafCert := createLeafCert(t, intermediateCaCert, intermediateCaKey, "")
				rootCrl := createCrl(t, rootCaCert, rootCaPrivateKey, revokedCert(intermediateCaCert))

				server := createCrlServer(t, newCrlEndpointDef("/rootCrl", rootCrl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, intermediateCaCert, rootCaCert}})
				if checkMode == CertRevocationCheckEnabled {
					assertEqualE(t, err.Error(), "every verified certificate chain contained revoked certificates")
				} else {
					assertEqualE(t, err.Error(), "every verified certificate chain contained revoked certificates")
				}
			})

			t.Run("CrlSignatureInvalid", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "/rootCrl")
				otherCaPrivateKey, _ := createCa(t, nil, nil, "other CA", "")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
				crl := createCrl(t, caCert, otherCaPrivateKey) // signed with wrong key

				server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				if checkMode == CertRevocationCheckEnabled {
					assertStringContainsE(t, err.Error(), "certificate revocation check failed")
				} else {
					assertNilE(t, err)
				}
			})

			t.Run("CrlIssuerMismatch", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "/rootCrl")
				otherKey, otherCert := createCa(t, nil, nil, "other CA", "")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
				crl := createCrl(t, otherCert, otherKey) // issued by other CA

				server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				if checkMode == CertRevocationCheckEnabled {
					assertStringContainsE(t, err.Error(), "certificate revocation check failed")
				} else {
					assertNilE(t, err)
				}
			})

			t.Run("CertWithNoCrlDistributionPoints", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "")

				cv := newTestCrlValidator(t, checkMode)
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				if checkMode == CertRevocationCheckEnabled {
					assertEqualE(t, err.Error(), "certificate revocation check failed")
				} else {
					assertNilE(t, err)
				}
			})

			t.Run("CertWithNoCrlDistributionPointsAllowed", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "")

				cv := newTestCrlValidator(t, checkMode, allowCertificatesWithoutCrlURLType(true))
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				assertNilE(t, err)
			})

			t.Run("DownloadCrlFailsOnUnparsableCrl", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")

				server := createCrlServer(t)
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode, &http.Client{
					Transport: &malformedCrlRoundTripper{},
				})
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				if checkMode == CertRevocationCheckEnabled {
					assertEqualE(t, err.Error(), "certificate revocation check failed")
				} else {
					assertNilE(t, err)
				}
			})

			t.Run("DownloadCrlFailsOn404", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")

				server := createCrlServer(t)
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				if checkMode == CertRevocationCheckEnabled {
					assertEqualE(t, err.Error(), "certificate revocation check failed")
				} else {
					assertNilE(t, err)
				}
			})

			t.Run("VerifyAgainstIdpExtensionWithDistributionPointMatch", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "/rootCrl")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")

				idpValue, err := asn1.Marshal(issuingDistributionPoint{
					DistributionPoint: distributionPointName{
						FullName: []asn1.RawValue{
							{Bytes: []byte(fmt.Sprintf("http://localhost:%v/rootCrl", testCrlServerPort))},
						},
					},
				})
				assertNilF(t, err)
				idpExtension := &pkix.Extension{
					Id:    idpOID,
					Value: idpValue,
				}

				crl := createCrl(t, caCert, caPrivateKey, idpExtension)

				server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				err = cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				assertNilE(t, err)
			})

			t.Run("TestVerifyAgainstIdpExtensionWithDistributionPointMismatch", func(t *testing.T) {
				caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "/rootCrl")
				_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")

				idpValue, err := asn1.Marshal(issuingDistributionPoint{
					DistributionPoint: distributionPointName{
						FullName: []asn1.RawValue{
							{Bytes: []byte(fmt.Sprintf("http://localhost:%v/otherCrl", testCrlServerPort))},
						},
					},
				})
				assertNilF(t, err)
				idpExtension := &pkix.Extension{
					Id:    idpOID,
					Value: idpValue,
				}

				crl := createCrl(t, caCert, caPrivateKey, idpExtension)

				server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				err = cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
				if checkMode == CertRevocationCheckEnabled {
					assertNotNilF(t, err)
					assertEqualE(t, err.Error(), "certificate revocation check failed")
				} else {
					assertNilE(t, err)
				}
			})

			t.Run("AnyValidChainCausesSuccess", func(t *testing.T) {
				caKey, caCert := createCa(t, nil, nil, "root CA", "/rootCrl")
				_, revokedLeaf := createLeafCert(t, caCert, caKey, "/rootCrl")
				_, validLeaf := createLeafCert(t, caCert, caKey, "/rootCrl")

				// CRL revokes only the first leaf
				crl := createCrl(t, caCert, caKey, revokedCert(revokedLeaf))
				server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				// First chain: revoked, second chain: valid
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{
					{revokedLeaf, caCert},
					{validLeaf, caCert},
				})
				assertNilE(t, err)
			})

			t.Run("OneChainIsRevokedAndOtherIsError", func(t *testing.T) {
				caKey, caCert := createCa(t, nil, nil, "root CA", "/rootCrl")
				_, revokedLeaf := createLeafCert(t, caCert, caKey, "/rootCrl")
				_, errorLeaf := createLeafCert(t, caCert, caKey, "/missingCrl")

				// CRL revokes only the first leaf
				crl := createCrl(t, caCert, caKey, revokedCert(revokedLeaf))
				server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
				defer closeServer(t, server)

				cv := newTestCrlValidator(t, checkMode)
				// First chain: revoked, second chain: valid
				err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{
					{revokedLeaf, caCert},
					{errorLeaf, caCert},
				})
				if checkMode == CertRevocationCheckEnabled {
					assertNotNilF(t, err)
					assertEqualE(t, err.Error(), "certificate revocation check failed")
				} else {
					assertNilE(t, err)
				}
			})

			t.Run("CacheTests", func(t *testing.T) {
				t.Run("should use in-memory cache", func(t *testing.T) {
					caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
					_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
					crl := createCrl(t, caCert, caPrivateKey)

					server := createCrlServer(t)
					defer closeServer(t, server)

					crt := newCountingRoundTripper(snowflakeNoOcspTransport)
					cv := newTestCrlValidator(t, checkMode, cacheValidityTimeType(10*time.Minute), &http.Client{
						Transport: crt,
					})
					downloadTime := time.Now().Add(-1 * time.Minute)
					cv.inMemoryCache[fullCrlURL("/rootCrl")] = &crlInMemoryCacheValueType{
						crl:          crl,
						downloadTime: &downloadTime,
					}
					err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
					assertNilE(t, err)
					assertEqualE(t, crt.totalRequests(), 0)
					_, err = os.Open(cv.crlURLToPath("/rootCrl"))
					assertErrIsE(t, err, os.ErrNotExist, "CRL file should not be created in the cache directory")
				})

				t.Run("should promote on-disk cache to memory and not modify on-disk entry", func(t *testing.T) {
					caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
					_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
					crl := createCrl(t, caCert, caPrivateKey)

					server := createCrlServer(t)
					defer closeServer(t, server)

					crt := newCountingRoundTripper(snowflakeNoOcspTransport)
					cv := newTestCrlValidator(t, checkMode, cacheValidityTimeType(10*time.Minute), &http.Client{
						Transport: crt,
					})

					assertNilF(t, os.WriteFile(cv.crlURLToPath(fullCrlURL("/rootCrl")), crl.Raw, 0600)) // simulate a cached CRL
					statBefore, err := os.Stat(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertNilF(t, err)

					err = cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
					assertNilE(t, err)
					assertEqualE(t, crt.totalRequests(), 0)
					statAfter, err := os.Stat(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertNilF(t, err)
					assertEqualE(t, statBefore.ModTime().Equal(statAfter.ModTime()), true, "CRL file should not be modified in the cache directory")
				})

				t.Run("should redownload when nextUpdate is reached", func(t *testing.T) {
					caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
					_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
					oldCrl := createCrl(t, caCert, caPrivateKey, thisUpdateType(time.Now().Add(-2*time.Minute)), nextUpdateType(time.Now().Add(-1*time.Minute)))
					newCrl := createCrl(t, caCert, caPrivateKey, thisUpdateType(time.Now()), nextUpdateType(time.Now().Add(time.Hour)))

					server := createCrlServer(t, newCrlEndpointDef("/rootCrl", newCrl))
					defer closeServer(t, server)

					crt := newCountingRoundTripper(snowflakeNoOcspTransport)
					cv := newTestCrlValidator(t, checkMode, cacheValidityTimeType(10*time.Minute), &http.Client{
						Transport: crt,
					})

					previousDownloadTime := time.Now().Add(-1 * time.Minute)
					cv.inMemoryCache[fullCrlURL("/rootCrl")] = &crlInMemoryCacheValueType{
						crl:          oldCrl,
						downloadTime: &previousDownloadTime,
					}

					err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
					assertNilE(t, err)

					assertEqualE(t, crt.totalRequests(), 1)
					fd, err := os.Open(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertNilE(t, err, "CRL file should be created in the cache directory")
					defer fd.Close()
					assertTrueE(t, cv.inMemoryCache[fullCrlURL("/rootCrl")].downloadTime.After(previousDownloadTime))
					assertTrueE(t, cv.inMemoryCache[fullCrlURL("/rootCrl")].crl.NextUpdate.Equal(newCrl.NextUpdate))
				})

				t.Run("should redownload when evicted in cache", func(t *testing.T) {
					caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
					_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
					oldCrl := createCrl(t, caCert, caPrivateKey, thisUpdateType(time.Now().Add(-2*time.Hour)), nextUpdateType(time.Now().Add(time.Hour)))
					newCrl := createCrl(t, caCert, caPrivateKey, thisUpdateType(time.Now()), nextUpdateType(time.Now().Add(4*time.Hour)))

					server := createCrlServer(t, newCrlEndpointDef("/rootCrl", newCrl))
					defer closeServer(t, server)

					crt := newCountingRoundTripper(snowflakeNoOcspTransport)
					cv := newTestCrlValidator(t, checkMode, cacheValidityTimeType(10*time.Minute), &http.Client{
						Transport: crt,
					})

					previousDownloadTime := time.Now().Add(-1 * time.Hour)
					cv.inMemoryCache[fullCrlURL("/rootCrl")] = &crlInMemoryCacheValueType{
						crl:          oldCrl,
						downloadTime: &previousDownloadTime,
					}

					err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
					assertNilE(t, err)

					assertEqualE(t, crt.totalRequests(), 1)
					fd, err := os.Open(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertNilE(t, err, "CRL file should be created in the cache directory")
					defer fd.Close()
					assertTrueE(t, cv.inMemoryCache[fullCrlURL("/rootCrl")].downloadTime.After(previousDownloadTime))
					assertTrueE(t, cv.inMemoryCache[fullCrlURL("/rootCrl")].crl.NextUpdate.Equal(newCrl.NextUpdate))
				})

				t.Run("should not save to on-disk cache when disabled", func(t *testing.T) {
					caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
					_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
					crl := createCrl(t, caCert, caPrivateKey)

					server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
					defer closeServer(t, server)

					cv := newTestCrlValidator(t, checkMode, onDiskCacheDisabledType(true))
					err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
					assertNilE(t, err)
					_, err = os.Open(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertErrIsE(t, err, os.ErrNotExist, "CRL file should not be created in the cache directory when on-disk cache is disabled")
					assertNotNilE(t, cv.inMemoryCache[fullCrlURL("/rootCrl")]) // in-memory cache should still be used
				})

				t.Run("should not read from on-disk cache when disabled", func(t *testing.T) {
					caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
					_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
					oldCrl := createCrl(t, caCert, caPrivateKey, nextUpdateType(time.Now()))
					newCrl := createCrl(t, caCert, caPrivateKey)

					server := createCrlServer(t, newCrlEndpointDef("/rootCrl", newCrl))
					defer closeServer(t, server)

					crt := newCountingRoundTripper(snowflakeNoOcspTransport)
					cv := newTestCrlValidator(t, checkMode, onDiskCacheDisabledType(true), &http.Client{
						Transport: crt,
					})
					assertNilF(t, os.WriteFile(cv.crlURLToPath(fullCrlURL("/rootCrl")), oldCrl.Raw, 0600)) // simulate a cached CRL
					statBefore, err := os.Stat(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertNilF(t, err)
					err = cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
					assertNilE(t, err)
					assertEqualE(t, crt.totalRequests(), 1, "CRL should be downloaded from the server")
					assertNotNilE(t, cv.inMemoryCache[fullCrlURL("/rootCrl")]) // in-memory cache should still be used
					statAfter, err := os.Stat(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertNilF(t, err)
					assertTrueE(t, statBefore.ModTime().Equal(statAfter.ModTime()), "CRL file should be modified in the cache directory")
				})

				t.Run("should not use in-memory cache when disabled", func(t *testing.T) {
					caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
					_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
					crl := createCrl(t, caCert, caPrivateKey)

					server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
					defer closeServer(t, server)

					cv := newTestCrlValidator(t, checkMode, inMemoryCacheDisabledType(true))
					err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
					assertNilE(t, err)
					assertNilE(t, cv.inMemoryCache, "in-memory cache should not be used when disabled")
					fd, err := os.Open(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertNilE(t, err) // on-disk cache should still be used
					defer fd.Close()
				})

				t.Run("should not use in-memory cache when disabled", func(t *testing.T) {
					caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
					_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
					crl := createCrl(t, caCert, caPrivateKey)

					server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
					defer closeServer(t, server)

					cv := newTestCrlValidator(t, checkMode, inMemoryCacheDisabledType(true), onDiskCacheDisabledType(true))
					err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
					assertNilE(t, err)
					assertNilE(t, cv.inMemoryCache[fullCrlURL("/rootCrl")], "in-memory cache should not be used when disabled")
					_, err = os.Open(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertErrIsE(t, err, os.ErrNotExist, "CRL file should not be created in the cache directory when on-disk cache is disabled")
				})

				t.Run("should clean up cache", func(t *testing.T) {
					caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "")
					_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
					crl := createCrl(t, caCert, caPrivateKey, nextUpdateType(time.Now().Add(10*time.Millisecond)))

					server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
					defer closeServer(t, server)

					cv := newTestCrlValidator(t, checkMode, cacheValidityTimeType(100*time.Millisecond))
					cv.onDiskCacheRemovalDelay = 200 * time.Millisecond
					cv.startPeriodicCacheCleanup(10 * time.Millisecond)
					defer cv.stopPeriodicCacheCleanup()

					err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
					assertNilE(t, err)
					cv.inMemoryCacheMutex.Lock()
					assertNotNilE(t, cv.inMemoryCache[fullCrlURL("/rootCrl")], "in-memory cache should be populated")
					cv.inMemoryCacheMutex.Unlock()
					fd, err := os.Open(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertNilE(t, err, "CRL file should be created in the cache directory")
					fd.Close()

					time.Sleep(500 * time.Millisecond) // wait for cleanup to happen

					cv.inMemoryCacheMutex.Lock()
					assertNilE(t, cv.inMemoryCache[fullCrlURL("/rootCrl")], "in-memory cache should be cleaned up")
					cv.inMemoryCacheMutex.Unlock()
					_, err = os.Open(cv.crlURLToPath(fullCrlURL("/rootCrl")))
					assertErrIsE(t, err, os.ErrNotExist, "CRL file should be removed from the cache directory")
				})
			})
		})
	}
}

func TestRealCrlWithIdpExtension(t *testing.T) {
	crlBytes, err := base64.StdEncoding.DecodeString(`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`)
	assertNilF(t, err)
	crl, err := x509.ParseRevocationList(crlBytes)
	assertNilF(t, err)
	cv := newTestCrlValidator(t, CertRevocationCheckEnabled)
	err = cv.verifyAgainstIdpExtension(crl, "http://c.pki.goog/we2/yK5nPhtHKQs.crl")
	assertNilE(t, err)
	err = cv.verifyAgainstIdpExtension(crl, "http://c.pki.goog/we2/other.crl")
	assertNotNilF(t, err)
	assertStringContainsE(t, err.Error(), "distribution point http://c.pki.goog/we2/other.crl not found in CRL IDP extension")
}

func TestParallelRequestToTheSameCrl(t *testing.T) {
	caPrivateKey, caCert := createCa(t, nil, nil, "root CA", "/rootCrl")
	_, leafCert := createLeafCert(t, caCert, caPrivateKey, "/rootCrl")
	crl := createCrl(t, caCert, caPrivateKey)

	server := createCrlServer(t, newCrlEndpointDef("/rootCrl", crl))
	defer closeServer(t, server)

	brt := newBlockingRoundTripper(snowflakeNoOcspTransport, 100*time.Millisecond)
	crt := newCountingRoundTripper(brt)
	cv := newTestCrlValidator(t, CertRevocationCheckEnabled, &http.Client{
		Transport: crt,
	})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := cv.verifyPeerCertificates(nil, [][]*x509.Certificate{{leafCert, caCert}})
			assertNilE(t, err)
		}()
	}
	wg.Wait()

	assertEqualE(t, crt.totalRequests(), 1)
}

func TestIsShortLivedCertificate(t *testing.T) {
	tests := []struct {
		name     string
		cert     *x509.Certificate
		expected bool
	}{
		{
			name: "Issued before March 15, 2024 (not short-lived)",
			cert: &x509.Certificate{
				NotBefore: time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC),
				NotAfter:  time.Date(2024, time.March, 10, 0, 0, 0, 0, time.UTC),
			},
			expected: false,
		},
		{
			name: "Issued after March 15, 2024, validity less than 10, but more than 7 days (short-lived)",
			cert: &x509.Certificate{
				NotBefore: time.Date(2024, time.March, 16, 0, 0, 0, 0, time.UTC),
				NotAfter:  time.Date(2024, time.March, 24, 0, 0, 0, 0, time.UTC),
			},
			expected: true,
		},
		{
			name: "Issued after March 15, 2024, validity less than 7 days (short-lived)",
			cert: &x509.Certificate{
				NotBefore: time.Date(2024, time.March, 16, 0, 0, 0, 0, time.UTC),
				NotAfter:  time.Date(2024, time.March, 22, 0, 0, 0, 0, time.UTC),
			},
			expected: true,
		},
		{
			name: "Issued after March 15, 2024, validity exactly 10 days (short-lived)",
			cert: &x509.Certificate{
				NotBefore: time.Date(2024, time.March, 16, 0, 0, 0, 0, time.UTC),
				NotAfter:  time.Date(2024, time.March, 26, 0, 0, 0, 0, time.UTC),
			},
			expected: true,
		},
		{
			name: "Issued after March 15, 2024, validity more than 10 days (not short-lived)",
			cert: &x509.Certificate{
				NotBefore: time.Date(2024, time.March, 16, 0, 0, 0, 0, time.UTC),
				NotAfter:  time.Date(2024, time.March, 27, 0, 0, 0, 0, time.UTC),
			},
			expected: false,
		},
		{
			name: "Issued after March 15, 2026, validity less than 7 days (short-lived)",
			cert: &x509.Certificate{
				NotBefore: time.Date(2026, time.March, 16, 0, 0, 0, 0, time.UTC),
				NotAfter:  time.Date(2026, time.March, 20, 0, 0, 0, 0, time.UTC),
			},
			expected: true,
		},
		{
			name: "Issued after March 15, 2026, validity exactly 7 days (short-lived)",
			cert: &x509.Certificate{
				NotBefore: time.Date(2026, time.March, 16, 0, 0, 0, 0, time.UTC),
				NotAfter:  time.Date(2026, time.March, 23, 0, 0, 0, 0, time.UTC),
			},
			expected: true,
		},
		{
			name: "Issued after March 15, 2026, validity more than 7 days (not short-lived)",
			cert: &x509.Certificate{
				NotBefore: time.Date(2026, time.March, 16, 0, 0, 0, 0, time.UTC),
				NotAfter:  time.Date(2026, time.March, 24, 0, 0, 0, 0, time.UTC),
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertEqualE(t, isShortLivedCertificate(tt.cert), tt.expected)
		})
	}
}

type malformedCrlRoundTripper struct {
}

func (m *malformedCrlRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	response := http.Response{
		StatusCode: http.StatusOK,
	}
	response.Body = http.NoBody
	return &response, nil
}

func createCa(t *testing.T, issuerCert *x509.Certificate, issuerPrivateKey *rsa.PrivateKey, cn string, crlEndpoint string) (*rsa.PrivateKey, *x509.Certificate) {
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization:       []string{"Snowflake"},
			OrganizationalUnit: []string{"Drivers"},
			Locality:           []string{"Warsaw"},
			CommonName:         cn,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	return createCert(t, caTemplate, issuerCert, issuerPrivateKey, crlEndpoint)
}

func createLeafCert(t *testing.T, issuerCert *x509.Certificate, issuerPrivateKey *rsa.PrivateKey, crlEndpoint string, params ...any) (*rsa.PrivateKey, *x509.Certificate) {
	notAfter := time.Now().AddDate(1, 0, 0)
	for _, param := range params {
		switch v := param.(type) {
		case notAfterType:
			notAfter = time.Time(v)
		}
	}
	serialNumber++
	certTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(serialNumber),
		Subject: pkix.Name{
			Organization:       []string{"Snowflake"},
			OrganizationalUnit: []string{"Drivers"},
			Locality:           []string{"Warsaw"},
			CommonName:         "localhost",
		},
		NotBefore: time.Now(),
		NotAfter:  notAfter,
		IsCA:      false,
	}
	return createCert(t, certTemplate, issuerCert, issuerPrivateKey, crlEndpoint)
}

func createCert(t *testing.T, template, issuerCert *x509.Certificate, issuerPrivateKey *rsa.PrivateKey, crlEndpoint string) (*rsa.PrivateKey, *x509.Certificate) {
	distributionPoints := []string{}
	if crlEndpoint != "" {
		distributionPoints = append(distributionPoints, fmt.Sprintf("http://localhost:%v%v", testCrlServerPort, crlEndpoint))
		template.CRLDistributionPoints = distributionPoints
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	assertNilF(t, err)
	signerPrivateKey := cmp.Or(issuerPrivateKey, privateKey)
	issuerCertOrSelfSigned := cmp.Or(issuerCert, template)
	certBytes, err := x509.CreateCertificate(rand.Reader, template, issuerCertOrSelfSigned, &privateKey.PublicKey, signerPrivateKey)
	assertNilF(t, err)
	cert, err := x509.ParseCertificate(certBytes)
	assertNilF(t, err)
	return privateKey, cert
}

func createCrl(t *testing.T, issuerCert *x509.Certificate, issuerPrivateKey *rsa.PrivateKey, args ...any) *x509.RevocationList {
	var revokedCertEntries []x509.RevocationListEntry
	var extensions []pkix.Extension
	thisUpdate := time.Now().Add(-time.Hour)
	nextUpdate := time.Now().Add(time.Hour)
	for _, arg := range args {
		switch v := arg.(type) {
		case revokedCert:
			revokedCertEntries = append(revokedCertEntries, x509.RevocationListEntry{
				SerialNumber:   v.SerialNumber,
				RevocationTime: time.Now().Add(-time.Hour * 24),
			})
		case *pkix.Extension:
			extensions = append(extensions, *v)
		case thisUpdateType:
			thisUpdate = time.Time(v)
		case nextUpdateType:
			nextUpdate = time.Time(v)
		default:
			t.Fatalf("unexpected argument type: %T", arg)
		}
	}
	crlTemplate := &x509.RevocationList{
		Number:                    big.NewInt(1),
		RevokedCertificateEntries: revokedCertEntries,
		ExtraExtensions:           extensions,
		ThisUpdate:                thisUpdate,
		NextUpdate:                nextUpdate,
	}
	crlBytes, err := x509.CreateRevocationList(rand.Reader, crlTemplate, issuerCert, issuerPrivateKey)
	assertNilF(t, err)
	crl, err := x509.ParseRevocationList(crlBytes)
	assertNilF(t, err)
	return crl
}

type crlEndpointDef struct {
	endpoint string
	crl      *x509.RevocationList
}

func newCrlEndpointDef(endpoint string, crl *x509.RevocationList) *crlEndpointDef {
	return &crlEndpointDef{
		endpoint: endpoint,
		crl:      crl,
	}
}

func createCrlServer(t *testing.T, endpointDefs ...*crlEndpointDef) *http.Server {
	mux := http.NewServeMux()
	for _, endpointDef := range endpointDefs {
		mux.HandleFunc(endpointDef.endpoint, func(responseWriter http.ResponseWriter, request *http.Request) {
			responseWriter.WriteHeader(http.StatusOK)
			_, err := responseWriter.Write(endpointDef.crl.Raw)
			assertNilF(t, err)
		})
	}
	server := &http.Server{
		Addr:    fmt.Sprintf(":%v", testCrlServerPort),
		Handler: mux,
	}
	go func() {
		err := server.ListenAndServe()
		assertErrIsF(t, err, http.ErrServerClosed)
	}()
	return server
}

func fullCrlURL(endpoint string) string {
	return fmt.Sprintf("http://localhost:%v%v", testCrlServerPort, endpoint)
}

func closeServer(t *testing.T, server *http.Server) {
	err := server.Shutdown(context.Background())
	assertNilF(t, err)
}

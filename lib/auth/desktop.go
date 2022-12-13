/*
Copyright 2021 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package auth

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"

	"github.com/gravitational/trace"

	"github.com/zmb3/teleport/api/client/proto"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/lib/modules"
	"github.com/zmb3/teleport/lib/tlsca"
)

// GenerateWindowsDesktopCert generates client certificate for Windows RDP
// authentication.
func (s *Server) GenerateWindowsDesktopCert(ctx context.Context, req *proto.WindowsDesktopCertRequest) (*proto.WindowsDesktopCertResponse, error) {
	if !modules.GetModules().Features().Desktop {
		return nil, trace.AccessDenied(
			"this Teleport cluster is not licensed for desktop access, please contact the cluster administrator")
	}
	csr, err := tlsca.ParseCertificateRequestPEM(req.CSR)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	clusterName, err := s.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	userCA, err := s.GetCertAuthority(ctx, types.CertAuthID{
		Type:       types.UserCA,
		DomainName: clusterName.GetClusterName(),
	}, true)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	caCert, signer, err := s.GetKeyStore().GetTLSCertAndSigner(ctx, userCA)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	tlsCA, err := tlsca.FromCertAndSigner(caCert, signer)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	// See https://docs.microsoft.com/en-us/troubleshoot/windows-server/windows-security/enabling-smart-card-logon-third-party-certification-authorities
	// for cert requirements for Windows authn.
	certReq := tlsca.CertificateRequest{
		Clock:     s.clock,
		PublicKey: csr.PublicKey,
		Subject:   csr.Subject,
		NotAfter:  s.clock.Now().UTC().Add(req.TTL.Get()),
		// Pass through ExtKeyUsage (which we need for Smartcard Logon usage)
		// and SubjectAltName (which we need for otherName SAN, not supported
		// out of the box in crypto/x509) extensions only.
		ExtraExtensions: filterExtensions(csr.Extensions, oidExtKeyUsage, oidSubjectAltName),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		// CRL is required for Windows smartcard certs.
		CRLDistributionPoints: []string{req.CRLEndpoint},
	}
	cert, err := tlsCA.GenerateCertificate(certReq)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return &proto.WindowsDesktopCertResponse{
		Cert: cert,
	}, nil
}

var (
	oidExtKeyUsage    = asn1.ObjectIdentifier{2, 5, 29, 37}
	oidSubjectAltName = asn1.ObjectIdentifier{2, 5, 29, 17}
)

func filterExtensions(extensions []pkix.Extension, oids ...asn1.ObjectIdentifier) []pkix.Extension {
	filtered := make([]pkix.Extension, 0, len(oids))
	for _, e := range extensions {
		for _, id := range oids {
			if e.Id.Equal(id) {
				filtered = append(filtered, e)
			}
		}
	}
	return filtered
}

// Copyright (c) Microsoft and contributors.  All rights reserved.
//
// This source code is licensed under the MIT license found in the
// LICENSE file in the root directory of this source tree.

package auth

import (
	"crypto/rsa"
	"crypto/x509"
	"fmt"
	"os"
	"regexp"

	"github.com/Azure/kubernetes-kms/pkg/config"

	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/adal"
	"github.com/Azure/go-autorest/autorest/azure"
	"golang.org/x/crypto/pkcs12"
	"k8s.io/klog/v2"
)

// GetKeyvaultToken() returns token for Keyvault endpoint
func GetKeyvaultToken(config *config.AzureConfig, env *azure.Environment) (authorizer autorest.Authorizer, err error) {
	kvEndPoint := env.KeyVaultEndpoint
	if '/' == kvEndPoint[len(kvEndPoint)-1] {
		kvEndPoint = kvEndPoint[:len(kvEndPoint)-1]
	}
	servicePrincipalToken, err := GetServicePrincipalToken(config, env, kvEndPoint)
	if err != nil {
		return nil, err
	}
	authorizer = autorest.NewBearerAuthorizer(servicePrincipalToken)
	return authorizer, nil
}

// GetServicePrincipalToken creates a new service principal token based on the configuration
func GetServicePrincipalToken(config *config.AzureConfig, env *azure.Environment, resource string) (*adal.ServicePrincipalToken, error) {
	oauthConfig, err := adal.NewOAuthConfig(env.ActiveDirectoryEndpoint, config.TenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth config, error: %v", err)
	}

	if config.UseManagedIdentityExtension {
		klog.V(2).Infof("using managed identity extension to retrieve access token")
		msiEndpoint, err := adal.GetMSIVMEndpoint()
		if err != nil {
			return nil, fmt.Errorf("failed to get managed service identity endpoint, error: %v", err)
		}
		// using user-assigned managed identity to access keyvault
		if len(config.UserAssignedIdentityID) > 0 {
			klog.V(2).InfoS("using User-assigned managed identity to retrieve access token", "clientID", redactClientCredentials(config.UserAssignedIdentityID))
			return adal.NewServicePrincipalTokenFromMSIWithUserAssignedID(msiEndpoint,
				env.ServiceManagementEndpoint,
				config.UserAssignedIdentityID)
		}
		klog.V(2).InfoS("using system-assigned managed identity to retrieve access token")
		// using system-assigned managed identity to access keyvault
		return adal.NewServicePrincipalTokenFromMSI(
			msiEndpoint,
			resource)
	}

	if len(config.ClientSecret) > 0 && len(config.ClientID) > 0 {
		klog.V(2).InfoS("azure: using client_id+client_secret to retrieve access token",
			"clientID", redactClientCredentials(config.ClientID), "clientSecret", redactClientCredentials(config.ClientSecret))

		return adal.NewServicePrincipalToken(
			*oauthConfig,
			config.ClientID,
			config.ClientSecret,
			resource)
	}

	if len(config.AADClientCertPath) > 0 && len(config.AADClientCertPassword) > 0 {
		klog.V(2).Infof("using jwt client_assertion (client_cert+client_private_key) to retrieve access token")
		certData, err := os.ReadFile(config.AADClientCertPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read client certificate from file %s, error: %v", config.AADClientCertPath, err)
		}
		certificate, privateKey, err := decodePkcs12(certData, config.AADClientCertPassword)
		if err != nil {
			return nil, fmt.Errorf("failed to decode the client certificate, error: %v", err)
		}
		return adal.NewServicePrincipalTokenFromCertificate(
			*oauthConfig,
			config.ClientID,
			certificate,
			privateKey,
			env.ServiceManagementEndpoint)
	}

	return nil, fmt.Errorf("no credentials provided for accessing keyvault")
}

// ParseAzureEnvironment returns azure environment by name
func ParseAzureEnvironment(cloudName string) (*azure.Environment, error) {
	var env azure.Environment
	var err error
	if cloudName == "" {
		env = azure.PublicCloud
	} else {
		env, err = azure.EnvironmentFromName(cloudName)
	}
	return &env, err
}

// decodePkcs12 decodes a PKCS#12 client certificate by extracting the public certificate and
// the private RSA key
func decodePkcs12(pkcs []byte, password string) (*x509.Certificate, *rsa.PrivateKey, error) {
	privateKey, certificate, err := pkcs12.Decode(pkcs, password)
	if err != nil {
		return nil, nil, fmt.Errorf("decoding the PKCS#12 client certificate: %v", err)
	}
	rsaPrivateKey, isRsaKey := privateKey.(*rsa.PrivateKey)
	if !isRsaKey {
		return nil, nil, fmt.Errorf("PKCS#12 certificate must contain a RSA private key")
	}

	return certificate, rsaPrivateKey, nil
}

// redactClientCredentials applies regex to a sensitive string and return the redacted value
func redactClientCredentials(sensitiveString string) string {
	r, _ := regexp.Compile(`^(\S{4})(\S|\s)*(\S{4})$`)
	return r.ReplaceAllString(sensitiveString, "$1##### REDACTED #####$3")
}

//go:build ignore

// Applies only disposable local bucket policies; administrator keys stay outside Files.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func main() {
	if len(os.Args) != 2 || configure(os.Args[1]) != nil {
		os.Stderr.WriteString("MM-43: bucket policy setup failed\n")
		os.Exit(1)
	}
}
func configure(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "credentials.json"))
	if err != nil {
		return err
	}
	var keys map[string]map[string]struct{ AccessKey, SecretKey string }
	if json.Unmarshal(data, &keys) != nil {
		return errors.New("invalid keys")
	}
	ca, err := os.ReadFile(filepath.Join(root, "pki/ca.crt"))
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("invalid CA")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, zone := range []struct{ name, endpoint, method string }{
		{"quarantine", "https://localhost:18343", "PUT"}, {"delivery-a", "https://localhost:18344", "GET"}, {"delivery-b", "https://localhost:18345", "GET"},
	} {
		key := keys[zone.name]["admin"]
		api := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(zone.endpoint), UsePathStyle: true, HTTPClient: client, RetryMaxAttempts: 1, Credentials: credentials.NewStaticCredentialsProvider(key.AccessKey, key.SecretKey, "")})
		_, err := api.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(zone.name)})
		var se smithy.APIError
		if err != nil && (!errors.As(err, &se) || (se.ErrorCode() != "BucketAlreadyOwnedByYou" && se.ErrorCode() != "BucketAlreadyExists")) {
			return err
		}
		_, err = api.PutBucketCors(ctx, &s3.PutBucketCorsInput{Bucket: aws.String(zone.name), CORSConfiguration: &types.CORSConfiguration{CORSRules: []types.CORSRule{{
			AllowedOrigins: []string{"https://localhost:8443"}, AllowedMethods: []string{zone.method}, MaxAgeSeconds: aws.Int32(60),
			AllowedHeaders: []string{"content-type", "content-length", "if-none-match", "x-amz-checksum-sha256", "x-amz-sdk-checksum-algorithm", "x-amz-server-side-encryption", "x-amz-server-side-encryption-aws-kms-key-id", "x-amz-meta-file-id", "x-amz-meta-upload-id"},
			ExposeHeaders:  []string{"etag", "content-disposition", "content-length", "x-amz-checksum-sha256"},
		}}}})
		if err != nil {
			return err
		}
		if zone.name == "quarantine" {
			_, err = api.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{Bucket: aws.String(zone.name), LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{{ID: aws.String("expire-source-after-processing-window"), Status: types.ExpirationStatusEnabled, Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")}, Expiration: &types.LifecycleExpiration{Days: aws.Int32(3)}, AbortIncompleteMultipartUpload: &types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(1)}}}}})
			if err != nil {
				return err
			}
			lifecycle, err := api.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(zone.name)})
			if err != nil || len(lifecycle.Rules) != 1 || lifecycle.Rules[0].Expiration == nil || aws.ToInt32(lifecycle.Rules[0].Expiration.Days) != 3 {
				return errors.New("lifecycle not persisted")
			}
		}
	}
	return nil
}

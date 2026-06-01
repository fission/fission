// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"context"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3ObjectStore is a minio-go/v7-backed objectStore. It replaces the previous
// github.com/graymeta/stow "s3" backend, dropping the transitive
// github.com/aws/aws-sdk-go v1 dependency.
//
// Object ids are the object key (path.Join(subDir, uuid)), exactly as stow/s3
// produced them, so archives created before an in-place upgrade keep resolving.
type s3ObjectStore struct {
	client *minio.Client
	bucket string
}

// newS3ObjectStore connects to the S3-compatible endpoint and ensures the
// bucket exists.
func newS3ObjectStore(endpoint, accessKeyID, secretAccessKey, region, bucket string) (*s3ObjectStore, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		// The previous stow/s3 backend set ConfigDisableSSL=true.
		Secure: false,
		Region: region,
	})
	if err != nil {
		return nil, err
	}

	store := &s3ObjectStore{client: client, bucket: bucket}
	if err := store.ensureBucket(context.Background(), region); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *s3ObjectStore) ensureBucket(ctx context.Context, region string) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	err = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: region})
	if err != nil {
		// A bucket we already own (e.g. created concurrently by another
		// storagesvc replica between our BucketExists check and here) is not
		// an error. "BucketAlreadyExists" is deliberately NOT treated as
		// success: in AWS S3 semantics it means the globally-unique name is
		// owned by a different account, which is a real misconfiguration.
		if minio.ToErrorResponse(err).Code == "BucketAlreadyOwnedByYou" {
			return nil
		}
		return err
	}
	return nil
}

func (s *s3ObjectStore) put(name string, r io.Reader, size int64) (string, error) {
	_, err := s.client.PutObject(context.Background(), s.bucket, name, r, size, minio.PutObjectOptions{})
	if err != nil {
		return "", err
	}
	// id is the object key, matching stow/s3's item.ID().
	return name, nil
}

func (s *s3ObjectStore) open(id string) (io.ReadCloser, error) {
	// minio's *Object is lazy: errors (including not-found) surface on the
	// first read or Stat. Stat here so callers get ErrNotFound up front,
	// matching the previous Item()-then-Open() flow.
	if _, err := s.size(id); err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(context.Background(), s.bucket, id, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapS3NotFound(err)
	}
	return obj, nil
}

func (s *s3ObjectStore) size(id string) (int64, error) {
	info, err := s.client.StatObject(context.Background(), s.bucket, id, minio.StatObjectOptions{})
	if err != nil {
		return 0, mapS3NotFound(err)
	}
	return info.Size, nil
}

func (s *s3ObjectStore) remove(id string) error {
	return s.client.RemoveObject(context.Background(), s.bucket, id, minio.RemoveObjectOptions{})
}

func (s *s3ObjectStore) list(prefix string) ([]objectInfo, error) {
	ctx := context.Background()
	infos := make([]objectInfo, 0)
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		infos = append(infos, objectInfo{id: obj.Key, lastMod: obj.LastModified})
	}
	return infos, nil
}

func (s *s3ObjectStore) exists(id string) (bool, error) {
	_, err := s.client.StatObject(context.Background(), s.bucket, id, minio.StatObjectOptions{})
	if err != nil {
		if isS3NotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// isS3NotFound reports whether err is a minio "object not found" error.
func isS3NotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == http.StatusNotFound
}

// mapS3NotFound translates a minio not-found error into ErrNotFound, leaving
// other errors untouched.
func mapS3NotFound(err error) error {
	if isS3NotFound(err) {
		return ErrNotFound
	}
	return err
}

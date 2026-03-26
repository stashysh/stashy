package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stashysh/stashy/internal/storage"
)

// Storage stores files in Amazon S3 or S3-compatible storage.
type Storage struct {
	client *s3.Client
	bucket string
}

func New(client *s3.Client, bucket string) *Storage {
	return &Storage{client: client, bucket: bucket}
}

func (s *Storage) Put(ctx context.Context, owner, contentType string, r io.Reader) (*storage.FileMeta, error) {
	id, err := storage.NewID()
	if err != nil {
		return nil, fmt.Errorf("generating id: %w", err)
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &id,
		Body:        r,
		ContentType: &contentType,
		Metadata: map[string]string{
			"owner": owner,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("putting object: %w", err)
	}

	return &storage.FileMeta{
		ID:          id,
		Owner:       owner,
		ContentType: contentType,
	}, nil
}

func (s *Storage) Get(ctx context.Context, id string) (io.ReadCloser, *storage.FileMeta, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &id,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, nil, fmt.Errorf("getting object: %w", err)
	}

	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}

	return out.Body, &storage.FileMeta{
		ID:          id,
		Owner:       out.Metadata["owner"],
		ContentType: aws.ToString(out.ContentType),
		Size:        size,
		Public:      out.Metadata["public"] == "true",
	}, nil
}

func (s *Storage) Update(ctx context.Context, id, owner, contentType string, r io.Reader) (*storage.FileMeta, error) {
	meta, err := s.headObject(ctx, id)
	if err != nil {
		return nil, err
	}

	if meta["owner"] != owner {
		return nil, fmt.Errorf("permission denied")
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &id,
		Body:        r,
		ContentType: &contentType,
		Metadata:    meta,
	})
	if err != nil {
		return nil, fmt.Errorf("putting object: %w", err)
	}

	return &storage.FileMeta{
		ID:          id,
		Owner:       owner,
		ContentType: contentType,
		Public:      meta["public"] == "true",
	}, nil
}

func (s *Storage) Delete(ctx context.Context, id, owner string) error {
	meta, err := s.headObject(ctx, id)
	if err != nil {
		return err
	}

	if meta["owner"] != owner {
		return fmt.Errorf("permission denied")
	}

	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &id,
	})
	if err != nil {
		return fmt.Errorf("deleting object: %w", err)
	}
	return nil
}

func (s *Storage) SetPublic(ctx context.Context, id string, public bool) error {
	meta, err := s.headObject(ctx, id)
	if err != nil {
		return err
	}

	if public {
		meta["public"] = "true"
	} else {
		delete(meta, "public")
	}

	// S3 requires a copy-in-place to update metadata.
	src := s.bucket + "/" + id
	_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            &s.bucket,
		Key:               &id,
		CopySource:        &src,
		Metadata:          meta,
		MetadataDirective: types.MetadataDirectiveReplace,
	})
	if err != nil {
		return fmt.Errorf("updating object metadata: %w", err)
	}
	return nil
}

func (s *Storage) headObject(ctx context.Context, id string) (map[string]string, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &id,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("heading object: %w", err)
	}
	if out.Metadata == nil {
		return make(map[string]string), nil
	}
	return out.Metadata, nil
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	return strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "NoSuchKey")
}

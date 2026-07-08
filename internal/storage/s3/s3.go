package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Storage stores file bytes in Amazon S3 or S3-compatible storage.
type Storage struct {
	client *s3.Client
	bucket string
}

func New(client *s3.Client, bucket string) *Storage {
	return &Storage{client: client, bucket: bucket}
}

func (s *Storage) Put(ctx context.Context, id string, r io.Reader) (int64, error) {
	cr := &countingReader{r: r}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &id,
		Body:   cr,
	})
	if err != nil {
		return 0, fmt.Errorf("putting object: %w", err)
	}
	return cr.n, nil
}

func (s *Storage) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &id,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("getting object: %w", err)
	}
	return out.Body, nil
}

func (s *Storage) GetRange(ctx context.Context, id string, start, length int64) (io.ReadCloser, error) {
	end := start + length - 1
	rangeHeader := fmt.Sprintf("bytes=%d-%d", start, end)

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &id,
		Range:  &rangeHeader,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("file not found: %s", id)
		}
		return nil, fmt.Errorf("getting object range: %w", err)
	}
	return out.Body, nil
}

func (s *Storage) Delete(ctx context.Context, id string) error {
	// S3 DeleteObject is idempotent: deleting a missing key succeeds.
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &id,
	})
	if err != nil {
		return fmt.Errorf("deleting object: %w", err)
	}
	return nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
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

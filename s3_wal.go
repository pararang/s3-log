package s3_log

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3WAL struct {
	client     *s3.Client
	bucketName string
	prefix     string
	length     uint64
}

func NewS3WAL(client *s3.Client, bucketName, prefix string) *S3WAL {
	return &S3WAL{
		client:     client,
		bucketName: bucketName,
		prefix:     prefix,
		length:     0,
	}
}

func (w *S3WAL) getObjectKey(offset uint64) string {
	return w.prefix + "/" + fmt.Sprintf("%020d", offset)
}

func calculateChecksum(buf *bytes.Buffer) [32]byte {
	return sha256.Sum256(buf.Bytes())
}

func validateChecksum(data []byte) bool {
	var storedChecksum [32]byte
	copy(storedChecksum[:], data[len(data)-32:])
	recordData := data[:len(data)-32]
	return storedChecksum == calculateChecksum(bytes.NewBuffer(recordData))
}

func prepareBody(offset uint64, data []byte) ([]byte, error) {
	// 8 bytes for offset, len(data) bytes for data, 32 bytes for checksum
	bufferLen := 8 + len(data) + 32
	buf := bytes.NewBuffer(make([]byte, 0, bufferLen))
	if err := binary.Write(buf, binary.BigEndian, offset); err != nil {
		return nil, err
	}
	if _, err := buf.Write(data); err != nil {
		return nil, err
	}
	checksum := calculateChecksum(buf)
	_, err := buf.Write(checksum[:])
	return buf.Bytes(), err
}

func (w *S3WAL) Append(ctx context.Context, data []byte) (uint64, error) {
	nextOffset := w.length + 1

	buf, err := prepareBody(nextOffset, data)
	if err != nil {
		return 0, fmt.Errorf("failed to prepare object body: %w", err)
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(w.bucketName),
		Key:         aws.String(w.getObjectKey(nextOffset)),
		Body:        bytes.NewReader(buf),
		IfNoneMatch: aws.String("*"),
	}

	if _, err = w.client.PutObject(ctx, input); err != nil {
		return 0, fmt.Errorf("failed to put object to S3: %w", err)
	}
	w.length = nextOffset
	return nextOffset, nil
}

func (w *S3WAL) Read(ctx context.Context, offset uint64) (Record, error) {
	key := w.getObjectKey(offset)
	input := &s3.GetObjectInput{
		Bucket: aws.String(w.bucketName),
		Key:    aws.String(key),
	}

	result, err := w.client.GetObject(ctx, input)
	if err != nil {
		return Record{}, fmt.Errorf("failed to get object from S3: %w", err)
	}
	defer result.Body.Close()

	data, _ := io.ReadAll(result.Body)
	if len(data) < 40 {
		return Record{}, fmt.Errorf("invalid record: data too short")
	}
	if !validateChecksum(data) {
		return Record{}, fmt.Errorf("checksum mismatch")
	}
	return Record{
		Offset: offset,
		Data:   data[8 : len(data)-32],
	}, nil
}

package storagesvc

import (
	"os"
	"path"

	"github.com/graymeta/stow"
	"github.com/graymeta/stow/s3"
	uuid "github.com/satori/go.uuid"
)

type (
	s3Storage struct {
		storageType     StorageType
		endpoint        string
		bucketName      string
		subDir          string
		accessKeyID     string
		secretAccessKey string
		region          string
	}
)

// NewS3Storage returns a new s3 storage struct
func NewS3Storage(args ...string) Storage {
	endpoint := os.Getenv("STORAGE_S3_ENDPOINT")
	bucketName := os.Getenv("STORAGE_S3_BUCKET_NAME")
	subDir := os.Getenv("STORAGE_S3_SUB_DIR")
	accessKeyID := os.Getenv("STORAGE_S3_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("STORAGE_S3_SECRET_ACCESS_KEY")
	region := os.Getenv("STORAGE_S3_REGION")

	return s3Storage{
		endpoint:        endpoint,
		storageType:     StorageTypeS3,
		bucketName:      bucketName,
		subDir:          subDir,
		accessKeyID:     accessKeyID,
		secretAccessKey: secretAccessKey,
		region:          region,
	}
}

func (ss s3Storage) getStorageType() StorageType {
	return ss.storageType
}

func (ss s3Storage) getContainerName() string {
	return ss.bucketName
}

func (ss s3Storage) getUploadFileName() string {
	uploadName := uuid.NewV4().String()
	return path.Join(ss.subDir, uploadName)
}

func (ss s3Storage) dial() (stow.Location, error) {
	kind := "s3"
	config := stow.ConfigMap{
		s3.ConfigEndpoint:    ss.endpoint,
		s3.ConfigAccessKeyID: ss.accessKeyID,
		s3.ConfigSecretKey:   ss.secretAccessKey,
		s3.ConfigRegion:      ss.region,
		s3.ConfigDisableSSL:  "true",
	}
	return stow.Dial(kind, config)
}

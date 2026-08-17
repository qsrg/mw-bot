// File storage.go: 文件存储抽象与实现，对齐 Python app/common/storage.py。
//
// 通过 FileStorage 接口隔离具体存储后端，业务模块只依赖接口，
// 本地默认 LocalFileStorage，生产可切换 MinioFileStorage。
package common

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// FileStorage 文件存储接口，所有实现必须满足该接口。
// 业务模块只依赖此接口，不直接耦合本地文件系统或 MinIO SDK。
type FileStorage interface {
	// Save 将输入流内容保存到 objectKey 指定的位置。
	Save(ctx context.Context, objectKey string, r io.Reader) error
	// Open 以读模式打开 objectKey，返回可关闭的读取流。
	Open(ctx context.Context, objectKey string) (io.ReadCloser, error)
	// Delete 删除 objectKey 指向的文件，不存在时静默。
	Delete(ctx context.Context, objectKey string) error
}

// LocalFileStorage 本地文件系统存储实现，用于本地开发。
// objectKey 即相对根目录的路径，会做路径越界校验避免目录穿越。
type LocalFileStorage struct {
	root string // 本地存储根目录
}

// NewLocalFileStorage 创建本地存储实例，确保根目录存在。
func NewLocalFileStorage(root string) (*LocalFileStorage, error) {
	if root == "" {
		return nil, fmt.Errorf("local storage root is empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create local storage root: %w", err)
	}
	return &LocalFileStorage{root: root}, nil
}

// resolve 将 objectKey 解析为绝对路径，并校验不越出根目录。
// 防止 `../` 等路径穿越攻击导致越界读写。
func (s *LocalFileStorage) resolve(objectKey string) (string, error) {
	if objectKey == "" {
		return "", fmt.Errorf("object key is empty")
	}
	absRoot, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("resolve storage root: %w", err)
	}
	full := filepath.Join(absRoot, objectKey)
	// 清理后再次校验前缀，避免 ../ 等相对片段越界
	cleaned := filepath.Clean(full)
	if !strings.HasPrefix(cleaned, absRoot+string(filepath.Separator)) && cleaned != absRoot {
		return "", fmt.Errorf("invalid object key: path escapes storage root")
	}
	return cleaned, nil
}

// Save 将输入流内容写到 objectKey 对应的本地文件。
// 自动创建父目录；若文件已存在则覆盖。
func (s *LocalFileStorage) Save(ctx context.Context, objectKey string, r io.Reader) error {
	path, err := s.resolve(objectKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()
	// 复制过程中关注 ctx 取消，避免大文件上传被遗弃
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write file content: %w", err)
	}
	return nil
}

// Open 以读模式打开本地文件，返回的 ReadCloser 由调用方负责关闭。
func (s *LocalFileStorage) Open(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	path, err := s.resolve(objectKey)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	return f, nil
}

// Delete 删除本地文件，不存在时静默；并清理空的文档目录（对齐 Python LocalFileStorage.delete_file，M9）。
func (s *LocalFileStorage) Delete(ctx context.Context, objectKey string) error {
	path, err := s.resolve(objectKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file: %w", err)
	}
	// 清理空文档目录（每个文档独占 uuid 目录，删文件后该目录无用途）
	absRoot, _ := filepath.Abs(s.root)
	if parent := filepath.Dir(path); parent != absRoot {
		_ = os.Remove(parent) // 目录非空或不存在时忽略
	}
	return nil
}

// MinioFileStorage MinIO 对象存储实现，用于生产环境。
// 通过 minio-go SDK 与 MinIO/S3 兼容服务交互。
type MinioFileStorage struct {
	client *minio.Client
	bucket string
}

// NewMinioFileStorage 创建 MinIO 存储实例。
// 初始化客户端并确保 bucket 存在（不存在则创建）。
func NewMinioFileStorage(endpoint, accessKey, secretKey, bucket string, secure bool) (*MinioFileStorage, error) {
	if endpoint == "" || accessKey == "" || secretKey == "" || bucket == "" {
		return nil, fmt.Errorf("minio config incomplete: endpoint/access_key/secret_key/bucket required")
	}
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}
	ctx := context.Background()
	// 启动时确保 bucket 存在；不存在则创建，避免运行期上传失败
	exists, err := cli.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("check minio bucket: %w", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("create minio bucket: %w", err)
		}
	}
	return &MinioFileStorage{client: cli, bucket: bucket}, nil
}

// Save 将输入流内容上传到 MinIO。objectKey 为对象键。
// 长度未知时用 -1，minio-go 会自动按 multipart 上传。
func (s *MinioFileStorage) Save(ctx context.Context, objectKey string, r io.Reader) error {
	if objectKey == "" {
		return fmt.Errorf("object key is empty")
	}
	if _, err := s.client.PutObject(ctx, s.bucket, objectKey, r, -1, minio.PutObjectOptions{}); err != nil {
		return fmt.Errorf("put minio object: %w", err)
	}
	return nil
}

// Open 从 MinIO 拉取对象，返回可关闭的读取流。
func (s *MinioFileStorage) Open(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	if objectKey == "" {
		return nil, fmt.Errorf("object key is empty")
	}
	obj, err := s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get minio object: %w", err)
	}
	return obj, nil
}

// Delete 删除 MinIO 对象，不存在时静默。
func (s *MinioFileStorage) Delete(ctx context.Context, objectKey string) error {
	if objectKey == "" {
		return fmt.Errorf("object key is empty")
	}
	if err := s.client.RemoveObject(ctx, s.bucket, objectKey, minio.RemoveObjectOptions{}); err != nil {
		// minio-go 在对象不存在时部分场景仍会返回错误，这里宽松处理避免业务报错
		return fmt.Errorf("remove minio object: %w", err)
	}
	return nil
}

// NewFileStorage 根据 Settings.StorageBackend 返回对应的存储实现。
// 支持 "local" 与 "minio" 两种后端，未知值返回错误。
func NewFileStorage(settings Settings) (FileStorage, error) {
	switch settings.StorageBackend {
	case "local":
		storage, err := NewLocalFileStorage(settings.LocalStorageRoot)
		if err != nil {
			return nil, err
		}
		return storage, nil
	case "minio":
		storage, err := NewMinioFileStorage(
			settings.MinioEndpoint,
			settings.MinioAccessKey,
			settings.MinioSecretKey,
			settings.MinioBucket,
			settings.MinioSecure,
		)
		if err != nil {
			return nil, err
		}
		return storage, nil
	default:
		return nil, fmt.Errorf("unsupported storage backend: %s", settings.StorageBackend)
	}
}

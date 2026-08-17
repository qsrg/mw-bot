// Package common 提供后端各模块共享的基础设施：配置、数据库、错误码、
// 安全（密码哈希与 JWT）、结构化日志、请求上下文与工具函数。
//
// 本文件 config.go 负责从环境变量读取所有配置项到 Settings 结构体，
// 字段名、环境变量名、默认值与必填规则与 Python app/common/config.py 对齐。
package common

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Settings 全局配置对象。非敏感字段提供本地开发默认值，敏感字段必须显式配置。
type Settings struct {
	// 应用基础信息
	Environment string // 运行环境（local/prod 等）
	AppPort     int    // 后端 HTTP 监听端口

	// 数据库与缓存
	DatabaseURL string // MySQL 连接 URL：mysql://user:pass@host:port/db
	RedisURL    string // Redis 连接 URL（可选，MVP 未直接使用）

	// 文件存储
	StorageBackend   string // 存储后端：local 或 minio
	LocalStorageRoot string // 本地存储根目录

	// 向量库
	ChromaPersistPath string // chromem-go 持久化目录

	// 检索
	HybridSearch           bool     // 是否启用混合检索（dense + BM25）
	RetrieveLimit          int      // 召回候选 chunk 数
	RetrieveScoreThreshold float64  // 检索相关性阈值（余弦相似度下限）
	RerankTopN             int      // rerank 后注入 prompt 的最终条数
	IntentDetection        bool     // 是否启用 LLM 意图精判
	Middlewares            []string // 中间件注册表（逗号分隔解析为切片）

	// 会话记忆
	HistoryTokenBudget int     // 会话短期记忆 token 预算
	HistoryRecentRatio float64 // 压缩后保留最近消息的比例

	// 模型网关（OpenAI 兼容）
	LLMBaseURL         string // LLM 网关地址
	LLMAPIKey          string // LLM 网关密钥
	LLMModel           string // LLM 模型名
	EmbeddingModel     string // Embedding 模型名
	EmbeddingDimension int    // Embedding 向量维度
	EmbeddingBaseURL   string // Embedding 网关地址（为空回退 LLMBaseURL）
	EmbeddingAPIKey    string // Embedding 网关密钥（为空回退 LLMAPIKey）

	// MinIO 对象存储
	MinioEndpoint  string // MinIO 端点
	MinioAccessKey string // MinIO 访问密钥
	MinioSecretKey string // MinIO 秘密密钥
	MinioBucket    string // MinIO 桶名
	MinioSecure    bool   // 是否启用 TLS

	// 认证（JWT）
	JWTSecret          string // JWT 签名密钥
	JWTAlgorithm       string // JWT 算法（HS256）
	AccessTokenMinutes int    // Access token 有效期（分钟）
}

// getenv 读取字符串环境变量，未设置或为空时返回默认值。
func getenv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// getenvRequired 读取必填字符串环境变量，未设置或为空时 log.Fatal 退出。
func getenvRequired(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

// getenvInt 读取整型环境变量，未设置时返回默认值，格式非法时 log.Fatal 退出。
func getenvInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("invalid integer for %s: %v", key, err)
	}
	return n
}

// getenvBool 读取布尔环境变量，未设置时返回默认值，格式非法时 log.Fatal 退出。
func getenvBool(key string, defaultVal bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("invalid boolean for %s: %v", key, err)
	}
	return b
}

// getenvFloat 读取浮点环境变量，未设置时返回默认值，格式非法时 log.Fatal 退出。
func getenvFloat(key string, defaultVal float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Fatalf("invalid float for %s: %v", key, err)
	}
	return f
}

// getenvDuration 读取时间段环境变量（如 "30s"、"5m"），未设置时返回默认值。
func getenvDuration(key string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("invalid duration for %s: %v", key, err)
	}
	return d
}

// getenvStringSlice 读取逗号分隔的字符串切片，去空白、转小写、去重。
func getenvStringSlice(key, defaultVal string) []string {
	raw := getenv(key, defaultVal)
	seen := make(map[string]bool)
	result := make([]string, 0)
	for _, name := range strings.Split(raw, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" && !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

// Load 从环境变量加载配置。敏感项缺失时 log.Fatal 退出。
func Load() Settings {
	return Settings{
		// 应用基础信息
		Environment: getenv("ENVIRONMENT", "local"),
		AppPort:     getenvInt("APP_PORT", 8080),

		// 数据库与缓存
		DatabaseURL: getenvRequired("DATABASE_URL"),
		RedisURL:    getenv("REDIS_URL", ""),

		// 文件存储
		StorageBackend:   getenv("STORAGE_BACKEND", "local"),
		LocalStorageRoot: getenv("LOCAL_STORAGE_ROOT", "data/uploads"),

		// 向量库
		ChromaPersistPath: getenv("CHROMA_PERSIST_PATH", "data/chroma"),

		// 检索
		HybridSearch:           getenvBool("HYBRID_SEARCH", true),
		RetrieveLimit:          getenvInt("RETRIEVE_LIMIT", 5),
		RetrieveScoreThreshold: getenvFloat("RETRIEVE_SCORE_THRESHOLD", 0.3),
		RerankTopN:             getenvInt("RERANK_TOP_N", 5),
		IntentDetection:        getenvBool("INTENT_DETECTION", false),
		Middlewares:            getenvStringSlice("MIDDLEWARES", "rocketmq,kafka,rabbitmq,pulsar,redis,nacos"),

		// 会话记忆
		HistoryTokenBudget: getenvInt("HISTORY_TOKEN_BUDGET", 100000),
		HistoryRecentRatio: getenvFloat("HISTORY_RECENT_RATIO", 0.7),

		// 模型网关
		LLMBaseURL:         getenvRequired("LLM_BASE_URL"),
		LLMAPIKey:          getenvRequired("LLM_API_KEY"),
		LLMModel:           getenv("LLM_MODEL", "qwen-plus"),
		EmbeddingModel:     getenv("EMBEDDING_MODEL", "text-embedding-v4"),
		EmbeddingDimension: getenvInt("EMBEDDING_DIMENSION", 1024),
		EmbeddingBaseURL:   getenv("EMBEDDING_BASE_URL", ""),
		EmbeddingAPIKey:    getenv("EMBEDDING_API_KEY", ""),

		// MinIO
		MinioEndpoint:  getenv("MINIO_ENDPOINT", "localhost:9000"),
		MinioAccessKey: getenvRequired("MINIO_ACCESS_KEY"),
		MinioSecretKey: getenvRequired("MINIO_SECRET_KEY"),
		MinioBucket:    getenv("MINIO_BUCKET", "ai-qa-documents"),
		MinioSecure:    getenvBool("MINIO_SECURE", false),

		// 认证
		JWTSecret:          getenvRequired("JWT_SECRET"),
		JWTAlgorithm:       getenv("JWT_ALGORITHM", "HS256"),
		AccessTokenMinutes: getenvInt("ACCESS_TOKEN_MINUTES", 120),
	}
}

// 注：原包级 settings 单例已移除。
// 中间件注册表 MiddlewareList 直接从环境变量 MIDDLEWARES 读取，避免包加载时
// 因必填环境变量（DATABASE_URL/LLM_API_KEY 等）缺失而 log.Fatal，影响单元测试。
// 业务模块通过 main.go 调用 common.Load() 显式获取 Settings 并注入。
// .env 配置文件加载见 loadenv.go 的 LoadEnvFile（供 main.go -config 标志使用）。

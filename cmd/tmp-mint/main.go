package main

import (
	"fmt"
	"os"

	"mw-bot/internal/common"
)

func main() {
	// 复用主程序同款 .env 加载：已存在环境变量优先
	common.LoadEnvFile("../../.env")
	settings := common.Load()
	tok, err := common.IssueToken(1, "preview-test", "admin", settings)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(tok)
}

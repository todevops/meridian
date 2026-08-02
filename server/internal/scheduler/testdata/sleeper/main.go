// sleeper 是调度器单测用的辅助二进制：
// 打印注入的 CMDB 环境变量、睡眠指定毫秒数后输出 CMDB_PRODUCED 声明并退出。
// 用法: sleeper <sleep_ms> [exit_code]
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	sleepMs := 0
	if len(os.Args) > 1 {
		sleepMs, _ = strconv.Atoi(os.Args[1])
	}
	exitCode := 0
	if len(os.Args) > 2 {
		exitCode, _ = strconv.Atoi(os.Args[2])
	}
	fmt.Printf("SECRET_TOKEN=%s\n", os.Getenv("TOKEN"))
	fmt.Printf("CONFIG_ENV=%s\n", os.Getenv("MY_CONFIG"))
	time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	fmt.Println("CMDB_PRODUCED=3")
	os.Exit(exitCode)
}

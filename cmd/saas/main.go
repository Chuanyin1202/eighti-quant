package main

// Phase 10 will implement the full SaaS entrypoint.
// This stub exists so go.mod can pin all required dependencies upfront.

import (
	_ "github.com/gin-gonic/gin"
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/gorilla/websocket"
	_ "github.com/redis/go-redis/v9"
	_ "github.com/robfig/cron/v3"
	_ "github.com/spf13/viper"
	_ "go.uber.org/zap"
	_ "gorm.io/driver/postgres"
	_ "gorm.io/gorm"
)

func main() {
	// Phase 10: see docs/系統總體拓撲結構.md §6.1 系統初始化
	panic("not implemented yet — see docs/系統總體拓撲結構.md §6.1")
}
